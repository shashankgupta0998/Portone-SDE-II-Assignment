# Progress log

A running log of how the pipeline was built and how each variance was isolated. Newest entries at
the bottom. Commit hashes refer to this repository's history.

## 1. Profiling the inputs (before writing code)

- **Payments CSV**: UTF-8 with BOM, 9 quoted disclaimer lines before the header (header on line 10),
  23,026 data rows, 25 columns, amounts with thousands separators (`-133,756.51`), timestamps like
  `29 June 2026 5:39:32 pm GMT+9`. Four settlement ids: 12395580393 (15,870 rows), 12382593803
  (5,649), 12407469483 (1,493), 12370691583 (14). 2,528 rows are `Deferred` with an empty
  `Transaction Release Date`.
- **Settlement TXT**: tab separated, one header line, then a settlement header row (line 2:
  settlement 12395580393, 17.07.2026 – 31.07.2026, total-amount 212,118.95 AUD, no amount),
  then 54,979 amount rows. Σ amount = 212,118.95, i.e. the file ties to its own header.
- **Configs**: 149 payment rules (column-oriented: one rule per type × description × amount column),
  137 settlement rules (one per type × amount-type × amount-description). Keys are UPPER_SNAKE;
  the raw files use mixed case and spaces, so a symmetric normalisation is needed.
- **Date token**: for order rows the settlement `posted-date-time` equals the payment
  `Transaction Release Date` converted to UTC, while payments `date/time` is the order date 7–20
  days earlier. Decision: the record_ref `date` of a payment row = UTC date of its release date
  (falling back to `date/time` when empty). This is a matching-logic interpretation; it is
  documented in the README and validated by the reconciliation counts below.

## 2. Build (commits `007a060` … `c364857`)

Built the pipeline stage by stage, each commit compiling on its own: schema + config tables →
streaming readers → mapping engine → verbatim config loader → stream/map/COPY with the summary
accumulated during ingestion → set-based reconciliation with self-checks → Excel report and
`trace` / `diagnose` → CLI → integration tests on PostgreSQL.

Issue found and fixed while building:

- The first full run reported **1,153 ingest errors**: every `Transaction Release Date` in August is
  written with an abbreviated month (`1 Aug 2026 6:41:19 pm GMT+9`) while the rest of the file uses
  full names (`18 July 2026 …`). Because unparseable cells are recorded in `ingest_errors` (and fail
  the `ingest_errors` self-check) rather than skipped, this showed up immediately. Fix: the parser
  accepts both month spellings (ingestion normalisation, not a data edit). After the fix: 0 errors,
  all 23,026 payment rows ingested.

## 3. Before-fix run (configs exactly as provided)

`make before` → `reports/report_before_fix.xlsx`.

Self-checks: streamed summary = SQL aggregate ✔; settlement Σ amount = header total-amount
(212,118.95) ✔; every payment row's columns add up to its total ✔; 0 ingest errors ✔; every amount
matched a rule group ✔.

Record counts (scope: settlement 12395580393): **reconciled 13,289 · unreconciled payment 2,407 ·
unreconciled settlement 0** · out-of-scope payment records 7,041 (rows of the three other
settlement ids, kept and listed separately).

Summary sheet, in scope (Payments − Settlements):

| Line | Payments | Settlements | Difference |
|---|---:|---:|---:|
| **Sales** | 452,890.54 | 357,801.39 | 95,089.15 |
| Product Charges | 419,826.77 | 348,908.77 | 70,918.00 |
| Shipping | 32,384.08 | 8,200.96 | 24,183.12 |
| Inventory Reimbursements | 679.69 | 679.69 | 0.00 |
| Other | 0.00 | 11.97 | −11.97 |
| **Refunds** | −1,910.51 | −1,815.09 | −95.42 |
| Refund expenses | 295.92 | 216.28 | 79.64 |
| Refunded sales | −2,206.43 | −2,031.37 | −175.06 |
| **Expenses** | −309,180.95 | −143,867.35 | −165,313.60 |
| Promo rebates | 0.00 | −11,273.53 | 11,273.53 |
| FBA fees | −0.41 | −0.41 | 0.00 |
| Amazon fees | −293,458.45 | −132,593.41 | −160,865.04 |
| Other | −15,722.09 | 0.00 | −15,722.09 |
| Paid To Amazon | 0.00 | 0.00 | 0.00 |
| Net of Summary lines | 141,799.08 | 212,118.95 | −70,319.87 |
| Routed to `sales_gift_wrap_credits` (no Summary line) | 22.04 | 0.00 | 22.04 |
| Not routed to any summary field | −19,918.61 | 0.00 | −19,918.61 |
| Net amount per source | 121,902.51 | 212,118.95 | −90,216.44 |

Lines not listed are 0 on both sides. Memo: in-scope `Deferred` payment rows total 43,540.07.

First observations from `recon diagnose` (leads to investigate, not yet conclusions):

- Two payment rule pairs apply to the same column: `ORDER,any,sales_tax_collected` (rules 70/71 →
  `sales_product_charges` and `sales_shipping`, 9,121 rows, 24,243.29 counted twice) and
  `ORDER,any,low_value_goods` (rules 4/5, 86 rows, −341.93 counted twice).
- The payout `Transfer` rows (`To account ending with: 334`) do not match the config keys
  `TO_ACCOUNT_ENDING` / `TO_YOUR_ACCOUNT_ENDING`; they fall through to the catch-all
  `,any,total → expenses_amazon_fees` and build a key with an empty `txn_ref`.
- The 2,407 unreconciled payment records are 2,376 deferred order records, 5 deferred refunds,
  25 released order records with a zero total, and the transfer.
- No settlement record is unreconciled.

The investigation of each variance follows in the next sections.

## 4. Investigation set-up

Before touching a rule I changed how the numbers are *presented*, not what they are, and
regenerated the before-fix report with the same code so before/after differ only by config
(commit `23ae7cb`):

- **Deferred rows are not part of this settlement.** In-scope payment rows split by
  `Transaction status`: Released rows sum (total) to 78,362.44; adding back the transfer
  (-133,756.51, see D1) gives **212,118.95 = Amazon's total-amount exactly**. The 2,398 Deferred
  rows (43,540.07) have no release date and none of their 2,381 record_refs exists in the
  settlement file. So the Payments column = Released rows; Deferred rows are shown per line in
  column F as a timing memo (D-M2), not mixed into the comparison.
- **New self-checks**, so "columns agree" is not the only test: an amount routed by two rules;
  amounts summarised through a fallback rule; routing to a field with no Summary line; net of the
  Summary lines vs Amazon total-amount (per source); and, per reconciled record, every summary
  field equal on both sides (catches misroutes that net to zero).

Before-fix, Released, settlement 12395580393 (P − S):

| Line | Payments | Settlements | P − S |
|---|---:|---:|---:|
| Product Charges | 348,815.93 | 348,908.77 | −92.84 |
| Shipping | 21,773.13 | 8,200.96 | 13,572.17 |
| Sales / Other | 0.00 | 11.97 | −11.97 |
| Refund expenses | 258.87 | 216.28 | 42.59 |
| Promo rebates | 0.00 | −11,273.53 | 11,273.53 |
| Expenses / Amazon fees | −266,349.92 | −132,593.41 | −133,756.51 |
| Expenses / Other | −11,273.53 | 0.00 | −11,273.53 |
| (no Summary line) `sales_gift_wrap_credits` | 11.61 | 0.00 | 11.61 |

Failing checks: `amount_routed_once` (9,207 row-columns, 23,901.36 counted twice, whole file),
`reconciled_fields_agree` (6,942 reconciled records disagree field by field even though every
reconciled record's *total* agrees — so record totals alone hide misroutes).
Net of Summary lines: payments 91,872.39 vs settlements 212,118.95 (= header).

Method for every variance below: decompose the settlement side by
(transaction-type, amount-type, amount-description) and the payment side by
(type, amount column, rule), find the combination whose sum equals the variance to the cent,
then confirm row by row on reconciled record_refs.

## 5. D1 — payout transfer booked as an Amazon fee (−133,756.51)

- Hypothesis from the size: −133,756.51 is not a sum of order fees; one payment row has that
  exact value: `amazon_payments_data.csv` line 16605, `Transfer`, "To account ending with: 334".
- Query: components of that row → rule level 4 (fallback `,any,total → expenses_amazon_fees`,
  config line 110). Config lines 62/63 (`TRANSFER,TO_ACCOUNT_ENDING` / `TO_YOUR_ACCOUNT_ENDING`)
  can never match: normalised description is `TO_ACCOUNT_ENDING_WITH:_334`.
- Ruled out: prefix matching in code. The schema has only exact and `any`; adding implicit prefix
  semantics would change how all 149 rules behave. `TRANSFER + any` expresses it in data, and the
  exact `MICRO_DEPOSIT` rules still win by precedence.
- Fix (MAPPING_FIXES.sql D1) → Expenses / Amazon fees P − S: −133,756.51 → **0.00**. The transfer
  remains an unreconciled payment (no transfer line in the settlement file, D-M3), now with a
  proper key `TRANSFER+TO_ACCOUNT_ENDING_WITH:_334+12395580393+2026-07-18` instead of
  `+12395580393+2026-07-18`.

## 6. D2 — Order promotional rebates in Expenses / Other (±11,273.53)

- Two lines move by the same amount in opposite directions (Promo rebates +11,273.53, Expenses /
  Other −11,273.53): a routing difference, not missing money.
- Settlement side: `Order / Promotion / Principal` −4,098.30 + `Order / Promotion / Shipping`
  −7,175.23 = −11,273.53 → `expenses_promotional_rebates` (settlement config lines 72/73).
  Payment side: column `promotional_rebates` of 3,983 Released order rows = −11,273.53 → rule
  line 65 → `expenses_other`. Equal to the cent.
- Ruled out: the settlement rule being the wrong one — the Summary has a dedicated "Promo rebates"
  line and the payments config itself routes refunded promotions to the refund promo family.
- Fix D2 → Promo rebates and Expenses / Other both **0.00**; `reconciled_fields_agree` drops from
  6,942 to 5,159 records.

## 7. D3 + D4 — duplicate payment rules (Shipping +13,572.17)

- `amount_routed_once` named the rows: the same `(row, column)` routed by two rules. Two pairs
  in the payments config, both in the `ORDER,any` group:
  `sales_tax_collected` (lines 71 → Product Charges and 72 → Shipping) and `low_value_goods`
  (lines 6 → Product Charges and 5 → Shipping).
- Shipping variance decomposed exactly: duplicate tax +13,675.59 + duplicate LVG −196.62 +
  (shipping-tax split, D5) +93.20 = **+13,572.17**.
- Ruled out "the second rule is meant for the shipping part of the tax": a column rule applies
  to the whole column; there is no way for it to take only the shipping share, so it can only
  double count.
- Fix D3 + D4 (delete lines 72 and 5) → Shipping +13,572.17 → +93.20; `amount_routed_once` passes;
  `reconciled_fields_agree` 5,159 → 819 records.

## 8. D5 — GST split per component on the settlement side (−92.84 / +93.20 / −0.36)

- What was left nets to zero inside Sales: Product Charges −92.84, Shipping +93.20, Other −0.36.
- Hypothesis: the payments `sales_tax_collected` column is the sum of four settlement components.
  Query over all 13,217 reconciled Order record_refs: payment `sales_tax_collected` =
  Σ settlement `ItemPrice/Tax + ItemPrice/ShippingTax + ItemPrice/GiftWrapTax +
  Promotion/TaxDiscount` for **every** record (0 mismatches; 13,675.59 both sides), and
  `low_value_goods` = Σ `LowValueGoodsTax-Principal + -Shipping` (0 mismatches; −196.62).
- So: ShippingTax 581.68 + GiftWrapTax 0.36 + TaxDiscount −672.16 + LVG-Shipping −2.72 = −92.84
  goes to Shipping / Other on the settlement side and can only go to one bucket on the payment
  side. Promotion/TaxDiscount → Shipping (line 95) is wrong on its own too: it is the tax
  discount of principal *and* shipping promotions.
- Options: (a) point the settlement tax components at Product Charges — ties, but books shipping
  and gift-wrap GST as product revenue; (b) route all order GST to the Summary's **Sales / Tax**
  line on both sides — ties, and makes Product Charges / Shipping GST-exclusive, which is what
  `product_sales` (= Principal 335,336.96) and `shipping_credits` (= Shipping 8,294.16) are.
  Chose (b).
- Fix D5 → Product Charges, Shipping, Tax all **0.00**; Sales / Other now −11.61 (D6 only);
  `reconciled_fields_agree` 819 → 19 records.

## 9. D6 + D7 — gift wrap off the Summary (−11.61) and refund GST not summarised (+42.59)

- `reconciled_fields_agree` now listed exactly 19 records; grouped by field they are two causes:
  - 3 records, `sales_gift_wrap_credits` 11.61 (P) vs `sales_other` 11.61 (S): payment lines
    5955, 14054, 14269 vs settlement lines 24104, 31121, 50305 (`ItemPrice/GiftWrap`). Payments
    rule line 70 routes gift wrap to a field no Summary line reads (`routed_fields_on_summary`
    flagged it from the first run).
  - 16 records, `refunded_expenses` P 88.34 vs S 45.75 (+42.59): refund rows' `sales_tax_collected`
    (−42.59, first line 14661) is not summarised (payments rule line 14, both buckets empty)
    while the settlement side routes `Refund ItemPrice/Tax −41.09 + ShippingTax −3.19 + Promotion/
    TaxDiscount +1.69 = −42.59` to `refunded_expenses`.
- Fix D6 (gift wrap → `sales_other`) and D7 (refund tax → `refunded_expenses`) → every Summary line
  **0.00**, `reconciled_fields_agree` **0 records**, net of Summary lines = 212,118.95 on both
  sides = Amazon total-amount. All 11 self-checks pass.

## 10. Latent defects (no row hits them here, 0.00 moved)

Read every rule against its siblings in both configs after the numbers tied, because a tie only
proves the rules that *these* rows hit:

- D8 payments `REFUND low_value_goods` (line 4) not summarised; settlement `REFUND ITEMWITHHELDTAX
  any` → refunded_expenses. Same gap as D7.
- D9 settlement `REFUND ITEMPRICE TAX` (line 4): positive → `total_refund_expense_or_sales_amt`
  (no Summary line), negative → refunded_expenses. All 13 lines here are negative.
- D10 settlement `PROMOTION_ADJUSTMENT` (line 138) record_ref
  `ADJUSTMENT_OTHER+settlement_id+date+record_type+settlement_id` can never equal the family key
  `ADJUSTMENT_OTHER+settlement_id+date` → would be a false unreconciled settlement.
- D11 payments `FEE_CORRECTION other` (line 31): negative bucket empty → negative corrections vanish.
- D12 settlement `FBA_LONG_TERM_STORAGE_FEE` (lines 134/135) uses `_` where every other spelling,
  including the rule's own record_ref, is `LONG-TERM` → would never match.

Looked at and not changed (reasons in MAPPING_FIXES.sql): the catch-all line 110 (no row reaches
it after D1; now reported by `fallback_rule_routed`), buckets with no Summary line that both
configs agree on (no rows), and FBA per-unit fees on Amazon fees (consistent on both sides; the
payments `fba_fees` column also holds chargebacks, so moving it would open a gap).

## 11. What remains, and why it cannot be closed

After-fix counts (settlement 12395580393): **reconciled 13,289 · unreconciled payment 2,407 ·
unreconciled settlement 0** (7,041 out-of-scope payment records of other settlement ids).

| Remaining item | Records | Amount | Why it stays |
|---|---:|---:|---|
| Deferred payment rows (D-M2) | 2,381 | 43,540.07 | Not released; no release date; no settlement line. Released + transfer = 212,118.95 proves they are outside this settlement. Shown per line in Summary column F. The 3 whose order id is in the settlement file are other SKUs of the same order (checked). |
| Payout transfer, line 16605 (D-M3) | 1 | −133,756.51 | A disbursement, not a settlement line; not summarised after D1. It is the only item between "net amount per source" (78,362.44) and Amazon's total (212,118.95). |
| Zero-value order lines (D-M4) | 25 | 0.00 | All amount columns 0; Amazon posts no settlement line; none of those order ids is in the settlement file. |
| Other settlement ids (D-M5) | 7,041 | 28,306.64 | Settlements 12370691583 / 12382593803 / 12407469483 are not in the settlement file. Listed on "Out of Scope Payments". |

The Summary's Payments − Settlements column is 0.00 on every line, and on top of that: every
reconciled record agrees field by field, nothing is routed twice, nothing reaches the catch-all,
nothing is routed off the Summary, and both columns' net equals Amazon's 212,118.95.
