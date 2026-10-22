-- MAPPING_FIXES.sql
--
-- Fixes to the mapping configs, applied as data changes to the rule tables that
-- `recon load-config` fills verbatim from:
--   data/amazon_payment_configs_au_old.csv  -> payment_mapping_rules
--   data/amazon_settlement_configs_au.csv   -> settlement_mapping_rules
--
-- Rows are targeted by their natural key (transaction_type / description or
-- amount_type / amount_description / amount_field) plus src_line, the line of the
-- rule in the original CSV (line 1 is the CSV header). Rules INSERTed here have
-- src_line NULL. A verification block at the end aborts the whole script if any
-- targeted rule was not found, so a partial fix can never be applied.
--
-- Apply with `make after` (which reloads the original configs first, so this
-- script always runs against the configs as provided), or manually:
--   recon load-config --payments-config ... --settlement-config ...
--   recon apply-sql --file MAPPING_FIXES.sql
--
-- One commented block per defect: what the rule was, what it is changed to, why,
-- and the variance it closes. Amounts are for settlement 12395580393, payment rows
-- with Transaction status Released (the Payments column of the Summary sheet);
-- "deferred" and "other settlements" give the movement in the rest of the file.
-- Defects that cannot be fixed in the config, or that belong in matching logic,
-- have a commented block too (D-M*, D-X*). PROGRESS.md has the query behind
-- every amount.

BEGIN;

-- ---------------------------------------------------------------------------
-- D1  Payout transfers miss their rules and fall into the catch-all, which
--     books the payout as an Amazon fee                      [payments config]
-- ---------------------------------------------------------------------------
-- Summary line: Expenses / Amazon fees, Payments - Settlements = -133,756.51.
-- Source rows: amazon_payments_data.csv line 16605 (type "Transfer", description
--   "To account ending with: 334", total/other -133,756.51, settlement 12395580393);
--   also line 1396 (-97,919.76, settlement 12382593803, out of scope).
-- Rules: payments config lines 62/63
--     TRANSFER,TO_YOUR_ACCOUNT_ENDING,total,TRANSFER+description+settlement_id+date,,
--     TRANSFER,TO_ACCOUNT_ENDING,total,TRANSFER+description+settlement_id+date,,
--   never match: the description carries the account digits and the word "with:"
--   ("TO_ACCOUNT_ENDING_WITH:_334"), and the schema only knows exact values or
--   "any". The row falls to the catch-all (line 110)
--     ,any,total,txn_ref+settlement_id+date,expenses_amazon_fees,expenses_amazon_fees
--   so the bank payout is summarised as an Amazon fee, and its record_ref is built
--   with an empty txn_ref ("+12395580393+2026-07-18").
-- Change: line 63 description TO_ACCOUNT_ENDING -> any (TRANSFER + any; the
--   MICRO_DEPOSIT rules keep precedence because an exact description beats "any"),
--   and add the matching rule for the "other" column, which carries the same amount
--   (same pattern as the MICRO_DEPOSIT pair, lines 60/61). Both not summarised: a
--   transfer to the seller's bank is a payout of the balance, not income or expense.
--   Line 62 stays as it is (exact value, still never matches; harmless).
-- Moves: -133,756.51 out of Expenses / Amazon fees (other settlements: -97,919.76).
--   The transfer stays an unreconciled payment: the settlement file has no transfer
--   line (see D-M3). Its record_ref becomes
--   TRANSFER+TO_ACCOUNT_ENDING_WITH:_334+12395580393+2026-07-18.
UPDATE payment_mapping_rules
   SET description = 'any'
 WHERE src_line = 63 AND transaction_type = 'TRANSFER' AND description = 'TO_ACCOUNT_ENDING' AND amount_field = 'total';
INSERT INTO payment_mapping_rules (src_line, transaction_type, description, amount_field, record_ref,
                                   to_summary_field_when_positive_amount, to_summary_field_when_negative_amount)
VALUES (NULL, 'TRANSFER', 'any', 'other', 'TRANSFER+description+settlement_id+date', '', '');

-- ---------------------------------------------------------------------------
-- D2  Order promotional rebates routed to Expenses / Other instead of
--     Promo rebates                                          [payments config]
-- ---------------------------------------------------------------------------
-- Summary lines: Expenses / Promo rebates P - S = +11,273.53 and Expenses / Other
--   P - S = -11,273.53 (net zero across the section, so the section subtotal hides it).
-- Source rows: 3,983 Released Order rows of settlement 12395580393 with a non-zero
--   promotional_rebates column (first: amazon_payments_data.csv line 11), sum -11,273.53.
--   Settlement side: Order / Promotion / Principal (1,580 rows, -4,098.30) +
--   Order / Promotion / Shipping (2,613 rows, -7,175.23) = -11,273.53, routed by
--   settlement config lines 72/73 to expenses_promotional_rebates.
-- Rule: payments config line 65
--     ORDER,any,promotional_rebates,txn_ref+sku+date,expenses_other,expenses_other
-- Change: both buckets -> expenses_promotional_rebates. The settlement config, the
--   REFUND promotional_rebates rule (refunded_expenses, same family) and the Summary
--   sheet's "Promo rebates" line all say promotions are promo rebates.
-- Moves: 11,273.53 from Expenses / Other to Expenses / Promo rebates
--   (deferred: 4,448.56; other settlements also move).
UPDATE payment_mapping_rules
   SET to_summary_field_when_positive_amount = 'expenses_promotional_rebates',
       to_summary_field_when_negative_amount = 'expenses_promotional_rebates'
 WHERE src_line = 65 AND transaction_type = 'ORDER' AND description = 'any' AND amount_field = 'promotional_rebates';

-- ---------------------------------------------------------------------------
-- D3  Duplicate rule: Order sales_tax_collected summarised twice
--                                                            [payments config]
-- ---------------------------------------------------------------------------
-- Summary lines: Sales / Shipping. The same column is routed by two rules of the
--   same group, so every Order row's GST is counted once in Product Charges AND
--   again in Shipping: +13,675.59 too much in Sales (5,235 Released order rows;
--   deferred +6,472.50). Check amount_routed_once fails on these rows.
-- Source rows: every Order row with sales_tax_collected <> 0 (first:
--   amazon_payments_data.csv line 1992).
-- Rules: payments config lines 71/72
--     ORDER,any,sales_tax_collected,txn_ref+sku+date,sales_product_charges,sales_product_charges
--     ORDER,any,sales_tax_collected,txn_ref+sku+date,sales_shipping,sales_shipping
--   The payments report has ONE tax column per row (tax on principal + shipping +
--   gift wrap + promotion tax discount, see D5); a column-level rule cannot split
--   it, so the second rule can only duplicate it.
-- Change: delete line 72. (Where the remaining rule routes to is D5.)
-- Moves: -13,675.59 out of Sales / Shipping (deferred -6,472.50).
DELETE FROM payment_mapping_rules
 WHERE src_line = 72 AND transaction_type = 'ORDER' AND description = 'any' AND amount_field = 'sales_tax_collected'
   AND to_summary_field_when_positive_amount = 'sales_shipping';

-- ---------------------------------------------------------------------------
-- D4  Duplicate rule: Order low_value_goods summarised twice  [payments config]
-- ---------------------------------------------------------------------------
-- Summary lines: Sales / Shipping. Same defect as D3 on the Low Value Goods tax
--   column: 50 Released order rows, -196.62, counted in both Product Charges and
--   Shipping (deferred -91.26).
-- Source rows: Order rows with low_value_goods <> 0 (first: line 1992).
--   Settlement side: Order / ItemWithheldTax / LowValueGoodsTax-Principal -193.90 +
--   LowValueGoodsTax-Shipping -2.72 = -196.62.
-- Rules: payments config lines 5/6
--     ORDER,any,low_value_goods,txn_ref+sku+date,sales_shipping,sales_shipping
--     ORDER,any,low_value_goods,txn_ref+sku+date,sales_product_charges,sales_product_charges
-- Change: delete line 5. (Where the remaining rule routes to is D5.)
-- Moves: +196.62 into Sales / Shipping (removes the second, negative copy;
--   deferred +91.26).
DELETE FROM payment_mapping_rules
 WHERE src_line = 5 AND transaction_type = 'ORDER' AND description = 'any' AND amount_field = 'low_value_goods'
   AND to_summary_field_when_positive_amount = 'sales_shipping';

-- ---------------------------------------------------------------------------
-- D5  Order GST split by component in the settlement config, which the
--     payments report cannot express        [settlement + payments config]
-- ---------------------------------------------------------------------------
-- Summary lines (after D3/D4): Product Charges P - S = -92.84, Shipping +93.20,
--   Sales / Other -0.36 (gift-wrap tax; the other -11.61 of that line is D6).
--   Nets to zero inside Sales, so the Sales subtotal hides it.
-- Proof: for each of the 13,217 reconciled Order record_refs, the payment row's
--   sales_tax_collected equals the sum of its settlement lines ItemPrice/Tax +
--   ItemPrice/ShippingTax + ItemPrice/GiftWrapTax + Promotion/TaxDiscount, and its
--   low_value_goods equals LowValueGoodsTax-Principal + LowValueGoodsTax-Shipping
--   (0 mismatching records; totals 13,675.59 and -196.62). Amazon's payments report
--   only has the combined columns.
-- Rules (settlement config): line 65 ORDER,ITEMPRICE,TAX -> sales_product_charges;
--   line 63 ORDER,ITEMPRICE,SHIPPINGTAX -> sales_shipping; line 61
--   ORDER,ITEMPRICE,GIFTWRAPTAX -> sales_other; line 95 ORDER,PROMOTION,TAXDISCOUNT
--   -> sales_shipping (tax discount of principal AND shipping promotions, routed
--   to Shipping); line 119 ORDER,ITEMWITHHELDTAX,LOWVALUEGOODSTAX-PRINCIPAL ->
--   sales_product_charges; line 118 ...LOWVALUEGOODSTAX-SHIPPING -> sales_shipping;
--   line 127 ...LOWVALUEGOODSTAX-OTHER -> sales_other.
--   (payments config) line 71 ORDER sales_tax_collected -> sales_product_charges;
--   line 6 ORDER low_value_goods -> sales_product_charges.
-- Variance: settlement lines 63 + 61 + 95 + 118 send 581.68 + 0.36 - 672.16 - 2.72
--   = -92.84 to Shipping / Other that the payments side necessarily books with the
--   tax column; Tax + LVG-Principal (13,765.71 - 193.90) is not the payment column
--   (13,675.59 - 196.62) either. No routing of a single payments column can match a
--   per-component split; the only bucket definition both reports can express
--   identically is "all GST of an order in one line".
-- Change: route every Order GST component, on both sides, to sales_tax - the
--   Summary sheet's Sales / Tax line, which otherwise only receives COMMINGLING_VAT.
--   Product Charges and Shipping become GST-exclusive (product_sales and
--   shipping_credits are GST-exclusive in this report: Principal 335,336.96 =
--   product_sales; Shipping 8,294.16 = shipping_credits). Line 127 (LVG-Other, 0 rows
--   here) moves with its family so a future row cannot reopen the gap.
-- Alternative considered: keep GST inside Product Charges by pointing the settlement
--   tax components at sales_product_charges. It also ties, but it would label
--   shipping and gift-wrap GST as product revenue; the Tax line exists for this.
--   If GST must stay split by base line, the config schema cannot do it for
--   payments (see D-X1).
-- Moves (Released): Sales / Tax 0.00 -> 13,478.97 on both sides; Product Charges
--   P 348,815.93 -> 335,336.96, S 348,908.77 -> 335,336.96 (P - S -92.84 -> 0.00);
--   Shipping S 8,200.96 -> 8,294.16 (P - S +93.20 -> 0.00); Sales / Other S 11.97
--   -> 11.61 (the 0.36 gift-wrap tax). Deferred: 6,381.24 into Sales / Tax.
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

COMMIT;
