# Amazon Payments vs Settlement reconciliation

A Go + PostgreSQL pipeline that ingests Amazon's **Payments** report (CSV) and **Settlement** report
(flat-file TSV) for the same settlement period, applies the two config-driven mapping tables,
reconciles the sources on the shared `record_ref`, and writes an accounting summary to Excel that is
tied out against Amazon's own settlement `total-amount`.

```
input files ──► ingest (stream) ──► mapping engine ──► summary accumulator ──► COPY into PostgreSQL
                                      ▲ rule tables (configs loaded verbatim, fixed by SQL)
PostgreSQL ──► reconcile (aggregate per record_ref, FULL OUTER JOIN) ──► self-checks ──► Excel report
```

Deliverables in this repository:

| Item | Where |
|---|---|
| Source code | `cmd/recon`, `internal/*` |
| Mapping fixes | [`MAPPING_FIXES.sql`](MAPPING_FIXES.sql) |
| Before-fix report (configs as provided) | `reports/report_before_fix.xlsx` |
| After-fix report | `reports/report_after_fix.xlsx` (produced by `make after`) |
| PostgreSQL dump | `db/recon_dump.sql.gz` (produced by `make dump`) |
| Progress log | [`PROGRESS.md`](PROGRESS.md) and the commit history |
| Input data, unmodified | `data/` (byte-identical copies of the files provided) |

## Results

Before fixes (configs exactly as provided), scope = settlement `12395580393`:

| Status | Records (distinct record_ref) |
|---|---:|
| Reconciled | 13,289 |
| Unreconciled payment | 2,407 |
| Unreconciled settlement | 0 |
| Out-of-scope payment (other settlement ids, reported separately) | 7,041 |

The before-fix Summary lines, their variances and the investigation are in
[`PROGRESS.md`](PROGRESS.md); the fixes are in [`MAPPING_FIXES.sql`](MAPPING_FIXES.sql).

## Running it

### Prerequisites

- Go 1.24+
- PostgreSQL 14+ (`docker compose up -d` starts PostgreSQL 16 with the credentials in `.env.example`)
- `pg_dump` only for `make dump`

```bash
cp .env.example .env          # DATABASE_URL, RECON_SCHEMA; the Makefile reads .env
make db-up                    # optional: PostgreSQL 16 in docker
make test                     # unit tests (integration tests skip without TEST_DATABASE_URL)
make test-integration         # integration tests against PostgreSQL (needs TEST_DATABASE_URL)
make before                   # configs as provided  -> reports/report_before_fix.xlsx
make after                    # configs + MAPPING_FIXES.sql -> reports/report_after_fix.xlsx
make dump                     # pg_dump of the recon schema -> db/recon_dump.sql.gz
```

`make before` / `make after` each run the whole flow end to end and are safe to repeat: they reload
the configs from the CSVs, re-ingest both files (replacing the previous ingestion), reconcile and
write the report. A full run over the real files takes ~20 s.

### The same flow, step by step

```bash
go build -o bin/recon ./cmd/recon
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/recon?sslmode=disable

bin/recon migrate                                   # create schema/tables (also done implicitly)
bin/recon load-config --payments-config data/amazon_payment_configs_au_old.csv \
                      --settlement-config data/amazon_settlement_configs_au.csv
bin/recon apply-sql --file MAPPING_FIXES.sql        # only for the after-fix run
bin/recon ingest    --payments data/amazon_payments_data.csv \
                    --settlements data/amazon_settlements_data.txt --label after-fix
bin/recon reconcile                                 # scope defaults to the settlement file's id(s)
bin/recon report    --out reports/report_after_fix.xlsx
```

`bin/recon run --payments … --settlements … --out … --label …` does ingest + reconcile + report in one
command. `recon` exits non-zero if any self-check fails (the report is still written so the failure
can be inspected).

### Auditing a number

```bash
bin/recon trace --summary-field expenses_amazon_fees --source payments --limit 20
bin/recon trace --record-ref '503-8856864-4518217+BIO-S000004059_AU+2026-07-17'
bin/recon trace --rule-id 70 --source payments
bin/recon diagnose          # rules never fired, double-applied amounts, unmapped/unruled amounts, empty key tokens
```

Each traced line shows the source file and physical line number, the rule id and its line in the
config CSV, the amount column, the summary field, the record_ref and the exact original line text.
The SQL view `v_component_trace` exposes the same join for ad-hoc queries; `sql/queries/` has the
queries used during the investigation.

### Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | — (required) | PostgreSQL connection string |
| `RECON_SCHEMA` | `recon` | Schema holding all tables (created if missing) |
| `TEST_DATABASE_URL` | — | Enables integration tests; each test uses a throwaway schema |

### Mock vs real mode

There is nothing to mock: the assignment involves no third-party services. PostgreSQL is required
infrastructure and is always used for real (locally, or via `docker-compose.yml`). Unit tests need no
database; integration tests run against a real PostgreSQL and skip cleanly when
`TEST_DATABASE_URL` is unset.

## Project structure

```
cmd/recon/            CLI: subcommand dispatch only
internal/config/      environment
internal/db/          connection (search_path = RECON_SCHEMA), embedded migrations, SQL script execution
  migrations/         0001 schema · 0002 Summary line layout · 0003 audit views
internal/ingest/      streaming readers for both files, parsers (amounts, timestamps), Row type
internal/rules/       verbatim config CSV → rule tables; rule tables → engine
internal/mapping/     normalisation, record_ref templates, date token, rule selection, sign routing
internal/summary/     summary accumulator fed while rows stream in
internal/store/       batched COPY of rows and mapped components, file replacement
internal/pipeline/    run lifecycle: ingest → reconcile → checks → history
internal/recon/       set-based reconciliation SQL and self-checks
internal/report/      Excel workbook (excelize)
internal/trace/       trace and diagnose queries (also feed the Exceptions sheet)
data/                 input files exactly as provided
testdata/             small hand-made fixtures used by the tests
sql/queries/          investigation queries
reports/              generated workbooks
```

## Schema and why

All tables live in one schema (`RECON_SCHEMA`, default `recon`); see
`internal/db/migrations/0001_schema.sql`.

**Ingestion — one table for both files.** `source_rows` holds every line of both reports, with a
`source` discriminator (`payments` / `settlements`) and a `row_kind` (`data`, `settlement_header`,
`error`). Columns the two reports share are normalised into common typed columns (`settlement_id`,
`transaction_type`, `description`, `order_id`, `sku`, `quantity`, `posted_at`, …); source-specific
fields are typed columns that are simply `NULL` for the other source (payments' 11 amount columns,
settlements' `amount`, `amount_type`, `shipment_id`, header totals). PostgreSQL stores `NULL`s almost
for free, so one sparse table is cheaper and clearer than an EAV model, and it satisfies the
single-table requirement without hiding types.

Every row keeps its **lineage**: `file_id` (→ `ingest_files`: name, sha256, size), the physical
`line_no` in the original file, `raw` (JSONB of original header → original cell text) and `raw_line`
(the exact line). Unparseable cells are recorded in `ingest_errors` with file + line; the row is still
stored (`row_kind = 'error'`) so nothing disappears silently. Preamble lines are logged as `info`.

Payment rows are stored **wide** (one row per CSV line, amounts in typed columns), not exploded at
ingest: the payment config is column-oriented (`amount_field`), and keeping the row intact means the
same raw row can be re-mapped after a config fix, and one file line = one table row.

**Configuration as data.** `payment_mapping_rules` and `settlement_mapping_rules` are loaded
verbatim from the CSVs (same column names, no trimming or case changes, `src_line` = line in the CSV,
ids follow file order). Fixes are SQL `UPDATE`/`INSERT`/`DELETE` against these tables
(`MAPPING_FIXES.sql`), never code. `summary_lines` maps each summary field to its Summary sheet line
and label, so the report layout is also data.

**Derived tables.**

| Table | Grain | Purpose |
|---|---|---|
| `mapped_components` | source row × applied rule | the audit spine: rule id + precedence level, amount column, amount, summary field, record_ref, status (`routed`, `not_summarised`, `zero`, `unmapped`, `unruled_column`), empty key tokens |
| `stream_summary` | run × source × settlement × status × field | totals computed while streaming |
| `recon_records` | record_ref × status | one Consolidated Data row: row counts, totals, representative row per side, line numbers, rule ids |
| `recon_record_buckets` | record_ref × side × field | per-bucket sums for the Consolidated sheet and differences |
| `runs`, `run_checks`, `run_history_summary` | run | config fingerprints (sha256 of rule tables), counts, self-check results, per-run summary totals (before/after evidence) |

Only the latest run is kept in full in the derived tables (they are rebuilt each run); `runs` and
`run_history_summary` keep every run's counts and totals, so the before → after delta survives in the
dump and is printed on the report's Run Info sheet.

## How the configs are applied

**Normalisation (symmetric).** Config keys and source values pass through the same function: trim,
en/em dash → `-`, upper-case, each whitespace run → `_`. Hyphens, colons, parentheses and `&` are
kept because config keys use them (`OTHER-TRANSACTION`, `LOST:WAREHOUSE`, `(FBA)`). Any remaining
spelling difference is treated as a config defect, not patched in code.

**Rule selection.** For each row the engine picks the most specific rule *group* that exists:

1. exact `transaction_type` + exact `description`
2. exact `transaction_type` + `any`
3. empty `transaction_type` (fallback) + exact `description`
4. empty `transaction_type` + `any`

(For settlements "description" means `amount_type` exact + `amount_description` exact or `any`.)
Every rule in the group is then applied — payments: each rule to its `amount_field` column of the
row; settlements: to the row's single `amount`. Consequences, all deliberate:

- The fallback is a **row-level** catch-all for transaction types the config does not know. A typed
  row whose group lacks a rule for one of its non-zero columns produces an `unruled_column`
  component (listed on the Exceptions sheet) rather than falling through to the fallback, which would
  route e.g. the row's `total` a second time.
- Two rules in one group on the same column are both applied, as written; `recon diagnose` lists such
  double applications so they can be fixed in the config.
- Rows with no group at all produce `unmapped` components (counted, listed, never dropped).
- Rules on a column the file does not have (`marketplace_withheld_tax`) never fire and are listed.

**Sign routing.** Amount > 0 → `to_summary_field_when_positive_amount`; < 0 → `…_negative_amount`;
0 → no bucket (`zero`). An empty bucket means the amount is stored and keyed but not summarised
(`not_summarised`).

**record_ref.** The template is split on `+`. Field tokens resolve per source: `txn_ref` (order id),
`sku`, `date`, `settlement_id`, `description` (normalised description / amount-description),
`shipment_id`, `merchant_order_id`, `record_type` (normalised transaction type); every other token is
a literal kept verbatim (e.g. `GENERAL ADJUSTMENT`). A field that is empty for a row stays empty and
is recorded in `empty_tokens`; no substitute id is invented.

**The `date` token** is a UTC calendar date: settlements use `posted-date`; payments use the UTC date
of `Transaction Release Date` (when the amount was released into a settlement — this equals the
settlement's `posted-date-time` for order rows), falling back to `date/time` when the release date
is empty (`Deferred` rows). This interpretation lives in `mapping.RefDate`.

## Reconciliation

- **Scope**: the settlement id(s) in the settlement file (`12395580393`), overridable with
  `--settlement-id`. Payment rows of other settlement ids are ingested, mapped and listed on the
  *Out of Scope Payments* sheet with status `out_of_scope_payment`; they are not counted as
  unreconciled and do not enter the Summary's Payments column. Deferred rows of the in-scope settlement
  are not filtered.
- **Granularity**: components are aggregated per (side, record_ref) *before* matching — per-bucket
  sums, distinct source-row count, source line numbers, and a Total equal to the sum of the source
  amounts of the distinct rows behind the key (payments: the row's `total`; settlements: the row's
  `amount`). This handles one payment row ↔ many settlement rows (an order line vs its
  Principal/Tax/Commission/FBA-fee rows) and many ↔ one.
- **Match**: `FULL OUTER JOIN` on record_ref → `reconciled` (both sides), `unreconciled_payment`,
  `unreconciled_settlement`. A reconciled key whose amounts differ stays reconciled (the brief defines
  status by key); the differences are shown per bucket on the Consolidated sheet.
- **Counts** are distinct record_refs per status, printed by the CLI, stored on `runs`, and shown on
  the Run Info sheet.

## The report

| Sheet | Contents |
|---|---|
| **Summary** | Rows 1–36 reproduce the sample exactly (Sales / Refunds / Expenses sections with subtotals on the section rows, Paid To Amazon on row 32). Payments column = payment records only; Settlements column = settlement records only; values are the totals streamed during ingestion. Below, a **tie-out** block: net of Summary lines, amounts routed to fields with no Summary line, amounts not routed to any field, net amount per source, Amazon's settlement `total-amount`, and the difference — every value a database sum. |
| **Consolidated Data** | The sample's column layout (A..DA): DS1 payment fields + Total + 21 buckets, DS2 settlement fields + Total + 21 buckets, Reconciliation Status, DS1−DS2 total and per-bucket differences. One row per record_ref, ordered reconciled → unreconciled payments → unreconciled settlements. Audit columns after DA: record_ref, row counts and `file:line` lists per side, settlement amount types, rule ids, buckets with no Summary line. |
| **Out of Scope Payments** | Same layout for payment records of other settlement ids. |
| **Exceptions** | Status totals, unmapped / unruled amounts, rules that never fired, amounts applied by more than one rule, empty key tokens, fields with no Summary line, ingest errors. |
| **Run Info** | Run label, scope, input file sha256s, rule-table fingerprints, counts, self-checks, and the summary totals of every run in the database (before vs after). |

Descriptive DS1/DS2 columns show the first source row of a record; amounts are sums over all its rows.

## Self-checks (every run)

- Streamed summary totals == SQL aggregate of stored components, for every (source, settlement,
  status, field).
- Settlement Σ `amount` == header `total-amount`, per settlement in scope.
- Each payment row's amount columns add up to its `total`.
- No unparseable cells (`ingest_errors`).
- Unmapped / unruled amounts (informational; itemised on the Exceptions sheet).

## Testing

- Unit tests: parsers (BOM, preamble, quoted commas, multi-line fields, thousands separators, GMT+9
  and abbreviated months → UTC, settlement dates), normalisation (case, spaces, en dash), record_ref
  templates (literals with spaces, repeated tokens, empty tokens), rule precedence L1–L4, wildcard,
  fallback only for unknown types, duplicate rules, unruled columns, sign routing, summary accumulator.
- Integration tests (`TEST_DATABASE_URL`): end-to-end on fixtures with 1:N and N:1 records, a deferred
  row, a transfer, a settlement-only fee and an out-of-scope row → expected counts, ordering, Summary
  cells and sheet layout; re-ingest idempotency; a config fix applied as SQL changes the next run with
  no code change.

```bash
make lint && make test && make test-integration
```

## Assumptions

- **Single table** applies to the ingested file rows; configs, mapped components and recon results are
  separate (derived) tables.
- **Ingest as provided**: the original files are read unmodified; all cleaning (BOM, preamble,
  quoting, thousands separators, month spellings, time zones, case) happens in code.
- **Scope** is the settlement id(s) present in the settlement file. Payment rows of other settlement
  ids are kept and reported separately, not treated as unreconciled.
- **Deferred** payment rows of the in-scope settlement are not filtered out; they flow through the
  reconciliation and are explained rather than hidden.
- **Date token**: UTC date of the payment release timestamp vs settlement `posted-date` (above).
- **Zero amounts** carry no sign, so they route to no bucket.
- **Duplicate rules** in one group are applied as written and surfaced, not silently de-duplicated.
- **Fallback rules** (empty `transaction_type`) apply only to rows whose transaction type has no rule
  group of its own.
- **Summary field → line** mapping is held in `summary_lines`; five sheet lines have no config field
  routing to them (Sales: Cross-account Debt Adjustment, FBA Fees, Micro Deposit (Failed); Expenses:
  Shipping Charges, Cross-account Debt Adjustment) and show 0 unless a rule routes into their reserved
  field. Config fields with no sheet line (e.g. `sales_gift_wrap_credits`,
  `total_refund_expense_or_sales_amt`) are shown in the tie-out block, never folded into a line.
- **Section rows** (Sales, Refunds, Expenses) carry the subtotal of their lines; Paid To Amazon stands
  alone. Signs are kept as Amazon reports them; the difference column is Payments − Settlements.
- **Amazon's control total** is the settlement header `total-amount` (212,118.95 AUD).

## What I'd do with more time

- Keep full component detail per run (partitioned by run id) so the before-fix state is queryable
  in the same dump, not only via the before-fix workbook.
- Add explicit precedence/priority and "applies to sign" columns to the config schema so duplicate or
  overlapping rules are impossible to express by accident, plus a config linter that runs on load.
- Property-based tests for the record_ref builder and a golden-file test of the full workbook.
- Parallel parsing for much larger files (the COPY path is already batched; parsing is single-threaded).
- Currency handling for multi-currency settlements (this data is AUD only; amounts are not converted).
