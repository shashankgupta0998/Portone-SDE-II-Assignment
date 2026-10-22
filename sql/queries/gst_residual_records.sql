-- GST component residual (MAPPING_FIXES.sql D5): are the reconciled records that fail
-- reconciled_fields_agree in the main after-fix run exactly the records whose settlement GST
-- components land outside Product Charges, and do they differ by exactly those amounts?
-- Expected per record, from the raw settlement Order rows behind the record_ref (a key can also
-- carry Refund rows; their GST goes to the Refunds section on both sides and is not part of this):
--   ship = ItemPrice/ShippingTax + Promotion/TaxDiscount + ItemWithheldTax/LowValueGoodsTax-Shipping
--   gw   = ItemPrice/GiftWrapTax
--   Product Charges P - S = ship + gw,  Shipping P - S = -ship,  Other P - S = -gw
-- Usage: psql ... -v run_id=<after-fix run id> -f sql/queries/gst_residual_records.sql
WITH rec AS (
  SELECT record_ref FROM recon_records WHERE run_id = :run_id AND status = 'reconciled'),
comp AS (
  SELECT DISTINCT c.record_ref, r.id, upper(r.amount_type) AS at, upper(r.description) AS ad, r.amount
  FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
  JOIN rec USING (record_ref)
  WHERE c.run_id = :run_id AND c.source = 'settlements' AND upper(r.transaction_type) = 'ORDER'),
exp AS (
  SELECT record_ref,
         COALESCE(sum(amount) FILTER (WHERE (at, ad) IN (('ITEMPRICE', 'SHIPPINGTAX'), ('PROMOTION', 'TAXDISCOUNT'),
                                                         ('ITEMWITHHELDTAX', 'LOWVALUEGOODSTAX-SHIPPING'))), 0) AS ship,
         COALESCE(sum(amount) FILTER (WHERE (at, ad) = ('ITEMPRICE', 'GIFTWRAPTAX')), 0) AS gw
  FROM comp GROUP BY 1),
expected AS (
  SELECT record_ref, 'sales_product_charges' AS summary_field, ship + gw AS diff FROM exp
  UNION ALL SELECT record_ref, 'sales_shipping', -ship FROM exp
  UNION ALL SELECT record_ref, 'sales_other', -gw FROM exp),
actual AS (
  SELECT b.record_ref, b.summary_field,
         COALESCE(sum(b.amount) FILTER (WHERE b.side = 'payments'), 0)
       - COALESCE(sum(b.amount) FILTER (WHERE b.side = 'settlements'), 0) AS diff
  FROM recon_record_buckets b JOIN rec USING (record_ref)
  WHERE b.run_id = :run_id GROUP BY 1, 2),
e AS (SELECT * FROM expected WHERE diff <> 0),
a AS (SELECT * FROM actual WHERE diff <> 0)
SELECT
  (SELECT count(DISTINCT record_ref) FROM a)                                   AS failing_records,
  (SELECT count(*) FROM a)                                                     AS failing_record_fields,
  (SELECT count(DISTINCT record_ref) FROM e)                                   AS gst_residual_records,
  (SELECT count(*) FROM e)                                                     AS gst_residual_record_fields,
  (SELECT count(*) FROM a FULL JOIN e USING (record_ref, summary_field)
     WHERE a.diff IS DISTINCT FROM e.diff)                                     AS mismatching_record_fields,
  (SELECT sum(diff) FROM a WHERE summary_field = 'sales_product_charges')      AS product_charges_p_minus_s,
  (SELECT sum(diff) FROM a WHERE summary_field = 'sales_shipping')             AS shipping_p_minus_s,
  (SELECT sum(diff) FROM a WHERE summary_field = 'sales_other')                AS other_p_minus_s,
  (SELECT string_agg(DISTINCT summary_field, ', ') FROM a)                     AS fields_involved;
