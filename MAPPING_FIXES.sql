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
-- have a commented block too (D5, D-M*, D-X*). PROGRESS.md has the query behind
-- every amount.
--
-- MAPPING_FIXES_OPTION_GST.sql is a separate, optional reporting choice applied on
-- top of this file (`make after-gst`): it moves all Order GST to the Sales / Tax line
-- on both sides so that every Summary line ties. See D5 and README "Interpretation
-- choices".

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
-- Change: delete config line 72. The remaining rule (config line 71) stays on
--   sales_product_charges in these fixes; D5 explains the GST residual that leaves.
--   MAPPING_FIXES_OPTION_GST.sql (optional) moves it to sales_tax.
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
-- Change: delete config line 5. The remaining rule (config line 6) stays on
--   sales_product_charges in these fixes (see D5).
--   MAPPING_FIXES_OPTION_GST.sql (optional) moves it to sales_tax.
-- Moves: +196.62 into Sales / Shipping (removes the second, negative copy;
--   deferred +91.26).
DELETE FROM payment_mapping_rules
 WHERE src_line = 5 AND transaction_type = 'ORDER' AND description = 'any' AND amount_field = 'low_value_goods'
   AND to_summary_field_when_positive_amount = 'sales_shipping';

-- ---------------------------------------------------------------------------
-- D5  GST component split: not closable in the config schema
--                                           [settlement + payments config; no SQL]
-- ---------------------------------------------------------------------------
-- Not a defect fix, so there is no SQL here. Order GST is reported differently by the
-- two sources:
--   - The payments report has ONE sales_tax_collected column and ONE low_value_goods
--     column per row. A payments rule maps a whole column to one bucket.
--   - The settlement report splits the same GST into components, each with its own
--     rule: line 65 ORDER,ITEMPRICE,TAX -> sales_product_charges; line 63
--     ITEMPRICE,SHIPPINGTAX -> sales_shipping; line 61 ITEMPRICE,GIFTWRAPTAX ->
--     sales_other; line 95 PROMOTION,TAXDISCOUNT -> sales_shipping; line 119
--     ITEMWITHHELDTAX,LOWVALUEGOODSTAX-PRINCIPAL -> sales_product_charges; line 118
--     ...LOWVALUEGOODSTAX-SHIPPING -> sales_shipping; line 127 ...LOWVALUEGOODSTAX-OTHER
--     -> sales_other.
--   After D3/D4 the payments side routes both columns to sales_product_charges
--   (payments config lines 71 and 6). A column-level rule cannot split a column, so
--   whatever the settlement config sends to Shipping or Other stays as a residual.
-- Proof that the money is the same: for each of the 13,217 reconciled Order
--   record_refs, the payment row's sales_tax_collected equals the sum of its
--   settlement lines ItemPrice/Tax + ItemPrice/ShippingTax + ItemPrice/GiftWrapTax +
--   Promotion/TaxDiscount, and its low_value_goods equals LowValueGoodsTax-Principal +
--   LowValueGoodsTax-Shipping (0 mismatching records; totals 13,675.59 and -196.62).
-- Residual in reports/report_after_fix.xlsx (Released, P - S), from the settlement
--   components outside Product Charges: ShippingTax 581.68 + GiftWrapTax 0.36 +
--   TaxDiscount -672.16 + LowValueGoodsTax-Shipping -2.72 = -92.84:
--     Sales / Product Charges   -92.84
--     Sales / Shipping          +93.20
--     Sales / Other              -0.36   (gift-wrap tax)
--   It nets to 0.00 inside the Sales subtotal, and both columns still net to Amazon's
--   total-amount 212,118.95. Self-check reconciled_fields_agree fails on exactly these
--   805 reconciled records (1,610 record x field differences, all in these three
--   fields), each by exactly its own GST components:
--   sql/queries/gst_residual_records.sql, 0 mismatching record-fields.
-- Proper fix: a schema change (D-X1), e.g. an allocation rule type for payments.
-- Alternative that makes every line tie within the current schema:
--   MAPPING_FIXES_OPTION_GST.sql (apply after this file) routes every Order GST
--   component, on both sides, to the Summary's Sales / Tax line. That is a reporting
--   choice (it rewrites seven settlement rules that are not wrong on their own), so it
--   is kept out of the defect fixes.

-- ---------------------------------------------------------------------------
-- D6  Order gift-wrap credits routed to a field that has no Summary line
--                                                            [payments config]
-- ---------------------------------------------------------------------------
-- Summary line: Sales / Other P - S = -11.97 before this fix (P 0.00, S 11.97); the
--   payment amount is in sales_gift_wrap_credits, which no Summary line reads, so it
--   silently drops out of the Summary (check routed_fields_on_summary lists it).
--   The settlement 11.97 is gift wrap 11.61 + gift-wrap tax 0.36; this fix closes the
--   11.61. The 0.36 is GST (settlement config line 61 ITEMPRICE,GIFTWRAPTAX ->
--   sales_other) and stays as the Sales / Other part of the D5 residual: after this
--   fix Sales / Other is P 11.61, S 11.97, P - S -0.36 (0.00 only with
--   MAPPING_FIXES_OPTION_GST.sql).
-- Source rows: amazon_payments_data.csv lines 5955, 14054, 14269 (Released,
--   3.99 + 3.99 + 3.63 = 11.61); deferred lines 20803, 21550, 22886 (10.43).
--   Settlement side: amazon_settlements_data.txt lines 24104, 31121, 50305
--   (Order / ItemPrice / GiftWrap, 11.61) -> sales_other (settlement config line 66).
-- Rule: payments config line 70
--     ORDER,any,gift_wrap_credits,txn_ref+sku+date,sales_gift_wrap_credits,sales_gift_wrap_credits
-- Change: both buckets -> sales_other, the bucket the settlement config uses for the
--   same money and a line that exists on the Summary.
-- Moves: +11.61 into Sales / Other, P - S -11.97 -> -0.36 (deferred +10.43);
--   sales_gift_wrap_credits empties.
UPDATE payment_mapping_rules
   SET to_summary_field_when_positive_amount = 'sales_other', to_summary_field_when_negative_amount = 'sales_other'
 WHERE src_line = 70 AND transaction_type = 'ORDER' AND description = 'any' AND amount_field = 'gift_wrap_credits';

-- ---------------------------------------------------------------------------
-- D7  Refund GST not summarised on the payments side           [payments config]
-- ---------------------------------------------------------------------------
-- Summary line: Refunds / Refund expenses P - S = +42.59.
-- Source rows: 16 Released Refund rows with sales_tax_collected <> 0 (first:
--   amazon_payments_data.csv line 14661, -1.61), sum -42.59. Settlement side for
--   the same record_refs: Refund / ItemPrice / Tax -41.09 + ItemPrice / ShippingTax
--   -3.19 + Promotion / TaxDiscount +1.69 = -42.59, all routed to refunded_expenses
--   (settlement config lines 4, 11, 20).
-- Rule: payments config line 14
--     REFUND,any,sales_tax_collected,txn_ref+sku+date,,
--   Empty on both signs, so refunded GST never reaches the Summary from payments.
-- Change: both buckets -> refunded_expenses, as the settlement config does for every
--   refund tax component. (Refunds keep the Refunds-section convention both configs
--   already use for refund components; unlike Order GST, D5, the settlement config
--   sends every refund tax component to the same bucket, so a single payments column
--   can match it.)
-- Moves: -42.59 into Refund expenses (deferred -15.81).
UPDATE payment_mapping_rules
   SET to_summary_field_when_positive_amount = 'refunded_expenses', to_summary_field_when_negative_amount = 'refunded_expenses'
 WHERE src_line = 14 AND transaction_type = 'REFUND' AND description = 'any' AND amount_field = 'sales_tax_collected';

-- ===========================================================================
-- Latent defects: rules no row of these files hits (0.00 moved here), but that
-- are wrong on their face and would misreport the first time a row arrives.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- D8  Refund low-value-goods tax not summarised on the payments side
--                                                            [payments config]
-- ---------------------------------------------------------------------------
-- Rule: payments config line 4  REFUND,any,low_value_goods,txn_ref+sku+date,,
-- Counterpart: settlement config line 120 REFUND,ITEMWITHHELDTAX,any ->
--   refunded_expenses. Same gap as D7 for the LVG column: a refunded LVG amount
--   would be in Refund expenses on the settlement side and nowhere on payments.
-- Change: both buckets -> refunded_expenses.  Moves: 0.00 (no refund row has
--   low_value_goods <> 0 in this file).
UPDATE payment_mapping_rules
   SET to_summary_field_when_positive_amount = 'refunded_expenses', to_summary_field_when_negative_amount = 'refunded_expenses'
 WHERE src_line = 4 AND transaction_type = 'REFUND' AND description = 'any' AND amount_field = 'low_value_goods';

-- ---------------------------------------------------------------------------
-- D9  Refund tax: positive amounts routed to a field with no Summary line
--                                                         [settlement config]
-- ---------------------------------------------------------------------------
-- Rule: settlement config line 4
--     REFUND,ITEMPRICE,TAX,txn_ref+sku+date,total_refund_expense_or_sales_amt,refunded_expenses
--   The sign split sends the same component to two different places, one of them
--   off the Summary. Every other refund tax component (SHIPPINGTAX line 11,
--   GIFTWRAPTAX line 12, PROMOTION TAXDISCOUNT line 20) is refunded_expenses on
--   both signs, and so is the payments side after D7.
-- Change: positive bucket -> refunded_expenses.  Moves: 0.00 (all 13 Refund /
--   ItemPrice / Tax lines in this file are negative).
UPDATE settlement_mapping_rules
   SET to_summary_field_when_positive_amount = 'refunded_expenses'
 WHERE src_line = 4 AND transaction_type = 'REFUND' AND amount_type = 'ITEMPRICE' AND amount_description = 'TAX'
   AND to_summary_field_when_positive_amount = 'total_refund_expense_or_sales_amt';

-- ---------------------------------------------------------------------------
-- D10 PROMOTION_ADJUSTMENT builds a record_ref no payment row can produce
--                                                         [settlement config]
-- ---------------------------------------------------------------------------
-- Rule: settlement config line 138
--     OTHER-TRANSACTION,OTHER-TRANSACTION,PROMOTION_ADJUSTMENT,
--     ADJUSTMENT_OTHER+settlement_id+date+record_type+settlement_id, ...
--   settlement_id appears twice and record_type is appended, so the key can never
--   equal ADJUSTMENT_OTHER+settlement_id+date, the key of the same family on both
--   sides (payments lines 2, 38/39, 150; settlement MISCADJUSTMENT line 28): every
--   such line would be a false unreconciled settlement.
-- Change: record_ref -> ADJUSTMENT_OTHER+settlement_id+date.  Moves: 0.00 (no
--   PROMOTION_ADJUSTMENT line in this file); reconciliation only, no Summary effect.
UPDATE settlement_mapping_rules
   SET record_ref = 'ADJUSTMENT_OTHER+settlement_id+date'
 WHERE src_line = 138 AND transaction_type = 'OTHER-TRANSACTION' AND amount_type = 'OTHER-TRANSACTION'
   AND amount_description = 'PROMOTION_ADJUSTMENT';

-- ---------------------------------------------------------------------------
-- D11 FBA inventory fee correction: negative amounts silently dropped
--                                                            [payments config]
-- ---------------------------------------------------------------------------
-- Rule: payments config line 31
--     ADJUSTMENT,FBA_INVENTORY_REIMBURSEMENT_-_FEE_CORRECTION,other,...,sales_other,
--   Positive corrections go to Sales / Other, negative ones to no bucket at all.
-- Change: negative bucket -> expenses_other (the sales_other / expenses_other pair
--   used for two-sided adjustments, e.g. SHIPPING_SERVICES,ADJUSTMENT line 108).
--   Moves: 0.00 (no fee-correction row in this file).
UPDATE payment_mapping_rules
   SET to_summary_field_when_negative_amount = 'expenses_other'
 WHERE src_line = 31 AND transaction_type = 'ADJUSTMENT' AND description = 'FBA_INVENTORY_REIMBURSEMENT_-_FEE_CORRECTION'
   AND amount_field = 'other' AND to_summary_field_when_negative_amount = '';

-- ---------------------------------------------------------------------------
-- D12 Long-term storage fee key spelled with "_" instead of "-"
--                                                         [settlement config]
-- ---------------------------------------------------------------------------
-- Rules: settlement config lines 134/135
--     FBAFEES,FBA_LONG_TERM_STORAGE_FEE,BASE_FEE / TAX_ON_FEE, record_ref
--     FBA_INVENTORY_FEE_FBA_LONG-TERM_STORAGE_FEE+settlement_id+date
--   Every other spelling of this fee in both configs - including these rules' own
--   record_ref and payments lines 50/51, 146/148 - is LONG-TERM, which is what a
--   source value "Long-Term" normalises to (hyphens are kept: OTHER-TRANSACTION,
--   SAFE-T, A-TO-Z depend on that). With "_" the rules would never match and the
--   fee would be unmapped.
-- Change: amount_type -> FBA_LONG-TERM_STORAGE_FEE.  Moves: 0.00 (no long-term
--   storage fee in this file, so the raw spelling could not be confirmed here).
UPDATE settlement_mapping_rules
   SET amount_type = 'FBA_LONG-TERM_STORAGE_FEE'
 WHERE src_line IN (134, 135) AND transaction_type = 'FBAFEES' AND amount_type = 'FBA_LONG_TERM_STORAGE_FEE';

-- ===========================================================================
-- Not config defects: matching logic, timing, and what cannot be closed.
-- No SQL in these blocks.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- D-M1 record_ref "date" of a payment row = Transaction Release Date (UTC)
--                                          [matching logic, internal/mapping/refdate.go]
-- ---------------------------------------------------------------------------
-- The configs only say "date". Settlement rows use posted-date. For payment rows,
-- date/time is the order date; it differs from the settlement posted date on
-- 13,328 of 13,353 Released order rows and 38 of 73 refund rows of settlement
-- 12395580393. Built from date/time, only 34 of 13,310 order/refund keys would
-- exist in the settlement file; built from the UTC date of Transaction Release
-- Date (the moment the money was posted to the settlement) all 13,289 reconcile.
-- Rows with no release date (Deferred) fall back to date/time.

-- ---------------------------------------------------------------------------
-- D-M2 Deferred payment rows are not part of this settlement     [timing; report]
-- ---------------------------------------------------------------------------
-- 2,398 payment rows (2,392 Order, 6 Refund) carry settlement id 12395580393 with
-- Transaction status Deferred and no release date: 43,540.07, posted 18-31 Jul
-- 2026. None of their 2,381 record_refs exists in the settlement file (the 3 whose
-- order id does appear there are other SKUs of the same order). Released rows of
-- the same id sum to 78,362.44, and 78,362.44 + 133,756.51 (the transfer, D-M3) =
-- 212,118.95 = Amazon's total-amount, so the deferred money is not in this
-- settlement; it is released into a later one. Cannot be closed with these files.
-- The Summary sheet shows it per line in column F; the records are listed as
-- unreconciled payments with reason "Deferred".

-- ---------------------------------------------------------------------------
-- D-M3 Payout transfer has no settlement line                       [cannot close]
-- ---------------------------------------------------------------------------
-- amazon_payments_data.csv line 16605 (-133,756.51, "To account ending with: 334",
-- 18 Jul 2026) is the bank payout made during this settlement period. The flat
-- settlement file lists amounts that make up the settlement, not disbursements,
-- so there is no line to match: 1 unreconciled payment, by construction. After D1
-- it is not summarised on any line.

-- ---------------------------------------------------------------------------
-- D-M4 Zero-value order lines                                       [cannot close]
-- ---------------------------------------------------------------------------
-- 25 Released Order rows of settlement 12395580393 have every amount column = 0
-- (e.g. lines 16266, 16267, 20412). No money moved, so Amazon posts no settlement
-- line and none of these order ids appears in the settlement file: 25 unreconciled
-- payments of 0.00 each.

-- ---------------------------------------------------------------------------
-- D-M5 Rows of other settlement ids                                  [out of scope]
-- ---------------------------------------------------------------------------
-- 7,156 payment rows belong to settlements 12370691583 (14), 12382593803 (5,649)
-- and 12407469483 (1,493): 28,306.64. The settlement file only covers 12395580393,
-- so they are kept, mapped and listed on "Out of Scope Payments" (7,041 records),
-- not counted as unreconciled. The fixes above change their routing too.

-- ---------------------------------------------------------------------------
-- D-X1 GST by base line is not expressible for payments          [schema change]
-- ---------------------------------------------------------------------------
-- Closing D5 while keeping GST inside Product Charges / Shipping / Other (as the
-- settlement config has it) is not possible for payments in the current schema: a
-- payments rule maps one whole column to one bucket, and the report has a single
-- sales_tax_collected column (and a single low_value_goods column). It would need a
-- schema change, e.g. an allocation rule type (a payment column split in the
-- proportions of the settlement components of the same record_ref) or a payments
-- source that reports tax per component. Without a schema change the only way to
-- make every line tie is to put all Order GST on one line on both sides:
-- MAPPING_FIXES_OPTION_GST.sql.

-- ---------------------------------------------------------------------------
-- D-X2 Catch-all and fields without a Summary line            [observation only]
-- ---------------------------------------------------------------------------
-- The payments catch-all (line 110, ,any,total -> expenses_amazon_fees) is what
-- turned D1 into a 133k "Amazon fee". After D1 no row reaches it; it is left as
-- provided because it is the documented fallback, and the pipeline now reports
-- every amount it summarises (check fallback_rule_routed). Several rules route to
-- fields with no Summary line (total_refund_expense_or_sales_amt,
-- total_adjustment_other_buyer_recharge_amt, current_reserve_amount,
-- beginning_balance, amazon_carried_forward); both configs agree on them, no row
-- in these files uses them, and if one does it is reported by check
-- routed_fields_on_summary and on the Summary tie-out block instead of vanishing.
-- Choosing their Summary line is a product decision, not a defect fix.

-- ---------------------------------------------------------------------------
-- Ruled out: FBA per-unit fulfilment fee on "Amazon fees" instead of "FBA fees"
-- ---------------------------------------------------------------------------
-- Both configs route it to expenses_amazon_fees (payments line 73, settlement line
-- 67). The payments fba_fees column also contains the shipping and gift-wrap
-- chargebacks (-1,198.94 and -11.97 here), which the settlement config also routes
-- to expenses_amazon_fees, so moving only the per-unit fee would open a gap that
-- the payments side cannot follow. Consistent on both sides; not changed.

-- ---------------------------------------------------------------------------
-- Guard: abort (and roll back everything above) if any targeted rule was missing.
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    bad text[] := ARRAY[]::text[];
BEGIN
    IF (SELECT count(*) FROM payment_mapping_rules WHERE transaction_type = 'TRANSFER' AND description = 'any') <> 2 THEN bad := array_append(bad, 'D1'); END IF;
    IF NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 65 AND to_summary_field_when_negative_amount = 'expenses_promotional_rebates') THEN bad := array_append(bad, 'D2'); END IF;
    IF EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 72) THEN bad := array_append(bad, 'D3'); END IF;
    IF EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 5) THEN bad := array_append(bad, 'D4'); END IF;
    IF NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 70 AND to_summary_field_when_positive_amount = 'sales_other') THEN bad := array_append(bad, 'D6'); END IF;
    IF NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 14 AND to_summary_field_when_negative_amount = 'refunded_expenses') THEN bad := array_append(bad, 'D7'); END IF;
    IF NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 4 AND to_summary_field_when_negative_amount = 'refunded_expenses') THEN bad := array_append(bad, 'D8'); END IF;
    IF NOT EXISTS (SELECT 1 FROM settlement_mapping_rules WHERE src_line = 4 AND to_summary_field_when_positive_amount = 'refunded_expenses') THEN bad := array_append(bad, 'D9'); END IF;
    IF NOT EXISTS (SELECT 1 FROM settlement_mapping_rules WHERE src_line = 138 AND record_ref = 'ADJUSTMENT_OTHER+settlement_id+date') THEN bad := array_append(bad, 'D10'); END IF;
    IF NOT EXISTS (SELECT 1 FROM payment_mapping_rules WHERE src_line = 31 AND to_summary_field_when_negative_amount = 'expenses_other') THEN bad := array_append(bad, 'D11'); END IF;
    IF (SELECT count(*) FROM settlement_mapping_rules WHERE src_line IN (134, 135) AND amount_type = 'FBA_LONG-TERM_STORAGE_FEE') <> 2 THEN bad := array_append(bad, 'D12'); END IF;
    IF cardinality(bad) > 0 THEN
        RAISE EXCEPTION 'MAPPING_FIXES.sql: fixes not applied as expected: % (were the configs reloaded first?)', bad;
    END IF;
END $$;

COMMIT;
