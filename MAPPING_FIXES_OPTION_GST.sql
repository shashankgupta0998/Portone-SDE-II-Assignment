-- MAPPING_FIXES_OPTION_GST.sql
--
-- OPTIONAL reporting choice, not a defect fix. Apply AFTER MAPPING_FIXES.sql:
--   recon load-config ...                     (configs as provided)
--   recon apply-sql --file MAPPING_FIXES.sql
--   recon apply-sql --file MAPPING_FIXES_OPTION_GST.sql
-- `make after-gst` does exactly this and writes reports/report_after_fix_gst_option.xlsx.
--
-- ---------------------------------------------------------------------------
-- GST option  Route all Order GST to Sales / Tax on both sides
--                                           [settlement + payments config]
-- ---------------------------------------------------------------------------
-- Why it is separate: MAPPING_FIXES.sql D5 explains that Order GST cannot be matched
--   line by line in the current config schema. The payments report has one
--   sales_tax_collected and one low_value_goods column per row, while the settlement
--   config routes each GST component to the line of its base amount (Tax and
--   LVG-Principal -> Product Charges; ShippingTax, Promotion/TaxDiscount and
--   LVG-Shipping -> Shipping; GiftWrapTax and LVG-Other -> Other). Those settlement
--   rules are not wrong on their own. This file rewrites them so both sides use the
--   only bucket definition both reports can express: "all GST of an order on one
--   line". The Summary sheet's Sales / Tax line exists for that and is otherwise
--   unused by Order rows.
-- What it changes:
--   payments config   line 71 ORDER,any,sales_tax_collected  sales_product_charges -> sales_tax
--                     line 6  ORDER,any,low_value_goods      sales_product_charges -> sales_tax
--   settlement config line 65 ORDER,ITEMPRICE,TAX                         -> sales_tax
--                     line 63 ORDER,ITEMPRICE,SHIPPINGTAX                 -> sales_tax
--                     line 61 ORDER,ITEMPRICE,GIFTWRAPTAX                 -> sales_tax
--                     line 95 ORDER,PROMOTION,TAXDISCOUNT                 -> sales_tax
--                     line 119 ORDER,ITEMWITHHELDTAX,LOWVALUEGOODSTAX-PRINCIPAL -> sales_tax
--                     line 118 ORDER,ITEMWITHHELDTAX,LOWVALUEGOODSTAX-SHIPPING  -> sales_tax
--                     line 127 ORDER,ITEMWITHHELDTAX,LOWVALUEGOODSTAX-OTHER     -> sales_tax
--   (line 127 has no rows here; it moves with its family so a future row cannot reopen
--   the gap). Both signs on every rule.
-- Effect, reports/report_after_fix.xlsx -> reports/report_after_fix_gst_option.xlsx
--   (Released, settlement 12395580393):
--     Sales / Product Charges  P 348,815.93 -> 335,336.96   S 348,908.77 -> 335,336.96   P - S -92.84 -> 0.00
--     Sales / Tax              P      0.00 ->  13,478.97   S      0.00 ->  13,478.97   P - S   0.00 -> 0.00
--     Sales / Shipping         P   8,294.16 (unchanged)    S   8,200.96 ->   8,294.16   P - S +93.20 -> 0.00
--     Sales / Other            P      11.61 (unchanged)    S      11.97 ->      11.61   P - S  -0.36 -> 0.00
--   Deferred memo (column F): 6,381.24 moves from Product Charges into Sales / Tax.
--   Product Charges and Shipping become GST-exclusive: Product Charges 335,336.96 =
--   settlement Principal = payments product_sales; Shipping 8,294.16 = payments
--   shipping_credits.
--   Self-check reconciled_fields_agree: 805 records -> 0, so every check passes.
-- Alternative considered: point the settlement GST components at Product Charges
--   instead. It also ties, but it would label shipping and gift-wrap GST as product
--   revenue; the Tax line is the more accurate label.

BEGIN;

-- Precondition: MAPPING_FIXES.sql must already be applied (its D3/D4 delete the duplicate
-- GST rules this option re-routes). Abort before changing anything otherwise.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line IN (72, 5) AND transaction_type = 'ORDER')
       OR NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 70 AND to_summary_field_when_positive_amount = 'sales_other') THEN
        RAISE EXCEPTION 'MAPPING_FIXES_OPTION_GST.sql: apply MAPPING_FIXES.sql first (reload configs, then MAPPING_FIXES.sql, then this file)';
    END IF;
END $$;

UPDATE payment_mapping_rules
   SET to_summary_field_when_positive_amount = 'sales_tax', to_summary_field_when_negative_amount = 'sales_tax'
 WHERE src_line IN (71, 6) AND transaction_type = 'ORDER' AND description = 'any'
   AND amount_field IN ('sales_tax_collected', 'low_value_goods')
   AND to_summary_field_when_positive_amount = 'sales_product_charges';
UPDATE settlement_mapping_rules
   SET to_summary_field_when_positive_amount = 'sales_tax', to_summary_field_when_negative_amount = 'sales_tax'
 WHERE transaction_type = 'ORDER'
   AND (src_line, amount_type, amount_description) IN (
        (65,  'ITEMPRICE',       'TAX'),
        (63,  'ITEMPRICE',       'SHIPPINGTAX'),
        (61,  'ITEMPRICE',       'GIFTWRAPTAX'),
        (95,  'PROMOTION',       'TAXDISCOUNT'),
        (119, 'ITEMWITHHELDTAX', 'LOWVALUEGOODSTAX-PRINCIPAL'),
        (118, 'ITEMWITHHELDTAX', 'LOWVALUEGOODSTAX-SHIPPING'),
        (127, 'ITEMWITHHELDTAX', 'LOWVALUEGOODSTAX-OTHER'));

-- Guard: abort (and roll back the updates above) unless every targeted rule moved.
DO $$
BEGIN
    IF (SELECT count(*) FROM payment_mapping_rules WHERE src_line IN (71, 6) AND transaction_type = 'ORDER'
          AND to_summary_field_when_positive_amount = 'sales_tax' AND to_summary_field_when_negative_amount = 'sales_tax') <> 2
       OR (SELECT count(*) FROM settlement_mapping_rules WHERE transaction_type = 'ORDER'
          AND to_summary_field_when_positive_amount = 'sales_tax' AND to_summary_field_when_negative_amount = 'sales_tax') <> 7 THEN
        RAISE EXCEPTION 'MAPPING_FIXES_OPTION_GST.sql: GST option not applied as expected (were the configs reloaded and MAPPING_FIXES.sql applied first?)';
    END IF;
END $$;

COMMIT;
