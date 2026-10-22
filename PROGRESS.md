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
