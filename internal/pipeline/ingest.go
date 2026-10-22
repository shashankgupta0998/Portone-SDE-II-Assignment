// Package pipeline wires the stages together: stream rows from a file, map them with the
// config engine, fold them into the streaming summary, and COPY rows + components to Postgres.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/mapping"
	"amazonrecon/internal/store"
	"amazonrecon/internal/summary"
)

// FileResult summarises one ingested file.
type FileResult struct {
	Source     string
	File       store.FileInfo
	FileID     int64
	DataRows   int
	HeaderRows int
	ErrorRows  int
	Skipped    int
	Components int
}

// IngestFile streams one file in a single transaction (all-or-nothing per file).
func IngestFile(ctx context.Context, pool *pgxpool.Pool, runID int64, source, path string,
	engine *mapping.Engine, acc *summary.Accumulator) (FileResult, error) {
	res := FileResult{Source: source}
	fi, err := store.Describe(path)
	if err != nil {
		return res, err
	}
	res.File = fi
	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer f.Close()

	var reader ingest.Reader
	switch source {
	case ingest.SourcePayments:
		reader, err = ingest.NewPaymentsReader(f)
	case ingest.SourceSettlements:
		reader, err = ingest.NewSettlementsReader(f)
	default:
		err = fmt.Errorf("unknown source %q", source)
	}
	if err != nil {
		return res, err
	}

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		fileID, err := store.ReplaceFile(ctx, tx, source, fi)
		if err != nil {
			return err
		}
		res.FileID = fileID
		w := store.NewWriter(tx, runID, fileID)
		for {
			row, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("%s: %w", fi.Name, err)
			}
			var comps []mapping.Component
			switch row.Kind {
			case ingest.KindData:
				row.RefDate = mapping.RefDate(row)
				comps = engine.Map(row)
				acc.Add(row, comps)
				res.DataRows++
				res.Components += len(comps)
			case ingest.KindSettlementHeader:
				res.HeaderRows++
			case ingest.KindError:
				res.ErrorRows++
			}
			if err := w.Add(ctx, row, comps); err != nil {
				return err
			}
		}
		if err := w.Flush(ctx); err != nil {
			return err
		}
		skipped := reader.Skipped()
		res.Skipped = len(skipped)
		if len(skipped) > 0 {
			vals := make([][]any, len(skipped))
			for i, s := range skipped {
				vals[i] = []any{fileID, s.LineNo, "info", "", "", s.Reason}
			}
			if err := store.WriteErrors(ctx, tx, vals); err != nil {
				return err
			}
		}
		return store.FinishFile(ctx, tx, fileID, res.DataRows+res.HeaderRows, res.Skipped, res.ErrorRows)
	})
	return res, err
}
