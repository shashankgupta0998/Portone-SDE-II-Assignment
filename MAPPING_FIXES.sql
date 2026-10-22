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

COMMIT;
