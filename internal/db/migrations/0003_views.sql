-- Audit views: every mapped amount joined back to its source file line and rule.
CREATE OR REPLACE VIEW v_component_trace AS
SELECT c.id              AS component_id,
       c.run_id,
       c.source,
       f.file_name,
       r.line_no,
       r.settlement_id,
       r.transaction_type,
       r.amount_type,
       r.description,
       r.order_id,
       r.sku,
       r.txn_status,
       r.ref_date,
       c.rule_id,
       c.rule_level,
       COALESCE(pr.src_line, sr.src_line) AS rule_src_line,
       c.amount_field,
       c.amount,
       c.summary_field,
       c.record_ref,
       c.status,
       c.empty_tokens,
       r.raw_line
FROM mapped_components c
JOIN source_rows r  ON r.id = c.source_row_id
JOIN ingest_files f ON f.id = r.file_id
LEFT JOIN payment_mapping_rules pr    ON c.source = 'payments'    AND pr.id = c.rule_id
LEFT JOIN settlement_mapping_rules sr ON c.source = 'settlements' AND sr.id = c.rule_id;

-- Per summary field and source: amount, component count and how many distinct source rows.
CREATE OR REPLACE VIEW v_summary_field_totals AS
SELECT c.source, c.summary_field, r.settlement_id,
       sum(c.amount) AS amount, count(*) AS components, count(DISTINCT c.source_row_id) AS source_rows
FROM mapped_components c
JOIN source_rows r ON r.id = c.source_row_id
WHERE c.status = 'routed'
GROUP BY 1, 2, 3;
