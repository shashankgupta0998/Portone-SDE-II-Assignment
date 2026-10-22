-- Unreconciled records grouped by the transaction type / status of their first source row.
SELECT rr.status, r.transaction_type, r.txn_status,
       CASE WHEN r.source = 'settlements' THEN r.amount_type || ' / ' || r.description
            WHEN r.transaction_type IN ('Order', 'Refund') THEN '(product line)'
            ELSE r.description END AS description,
       count(*) AS records, sum(COALESCE(rr.pay_total, rr.set_total)) AS amount
FROM recon_records rr
JOIN source_rows r ON r.id = COALESCE(rr.pay_first_row_id, rr.set_first_row_id)
WHERE rr.status IN ('unreconciled_payment', 'unreconciled_settlement')
GROUP BY 1, 2, 3, 4
ORDER BY 1, records DESC;
