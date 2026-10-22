-- Reconciled records whose amounts differ between the sides, per bucket (latest run).
SELECT b.record_ref, b.summary_field,
       COALESCE(sum(b.amount) FILTER (WHERE b.side = 'payments'), 0)    AS payments,
       COALESCE(sum(b.amount) FILTER (WHERE b.side = 'settlements'), 0) AS settlements
FROM recon_record_buckets b
JOIN recon_records rr ON rr.record_ref = b.record_ref AND rr.run_id = b.run_id AND rr.status = 'reconciled'
WHERE b.side IN ('payments', 'settlements')
GROUP BY 1, 2
HAVING COALESCE(sum(b.amount) FILTER (WHERE b.side = 'payments'), 0)
    <> COALESCE(sum(b.amount) FILTER (WHERE b.side = 'settlements'), 0)
ORDER BY 1, 2;
