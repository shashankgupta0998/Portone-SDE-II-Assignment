-- Payments vs Settlements per summary field for the latest run, in-scope settlement(s) only.
-- Run with: psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c 'SET search_path TO recon' -f sql/queries/summary_line_variance.sql
WITH run AS (SELECT id, scope_settlement_ids FROM runs ORDER BY id DESC LIMIT 1)
SELECT s.summary_field,
       COALESCE(sum(s.amount) FILTER (WHERE s.source = 'payments'), 0)    AS payments,
       COALESCE(sum(s.amount) FILTER (WHERE s.source = 'settlements'), 0) AS settlements,
       COALESCE(sum(s.amount) FILTER (WHERE s.source = 'payments'), 0)
     - COALESCE(sum(s.amount) FILTER (WHERE s.source = 'settlements'), 0) AS difference
FROM stream_summary s JOIN run ON s.run_id = run.id
WHERE s.settlement_id = ANY(run.scope_settlement_ids)
GROUP BY 1
ORDER BY abs(COALESCE(sum(s.amount) FILTER (WHERE s.source = 'payments'), 0)
           - COALESCE(sum(s.amount) FILTER (WHERE s.source = 'settlements'), 0)) DESC;
