-- Where does a summary-field variance come from? Payments-minus-settlements per summary field,
-- broken down by reconciliation status and source transaction type / description.
-- Usage: psql ... -v field=expenses_amazon_fees -f sql/queries/variance_by_record_type.sql
SELECT rr.status, b.side, r.transaction_type,
       CASE WHEN r.source = 'settlements' THEN r.amount_type || ' / ' || r.description
            WHEN r.transaction_type IN ('Order', 'Refund') THEN '(product line)'
            ELSE r.description END AS description,
       count(DISTINCT rr.record_ref) AS records, sum(b.amount) AS amount
FROM recon_record_buckets b
JOIN recon_records rr ON rr.record_ref = b.record_ref AND rr.run_id = b.run_id
     AND (b.side = 'settlements' OR rr.status <> 'out_of_scope_payment')
JOIN source_rows r ON r.id = CASE WHEN b.side = 'settlements' THEN rr.set_first_row_id ELSE rr.pay_first_row_id END
WHERE b.summary_field = :'field' AND b.side IN ('payments', 'settlements')
GROUP BY 1, 2, 3, 4
ORDER BY abs(sum(b.amount)) DESC;
