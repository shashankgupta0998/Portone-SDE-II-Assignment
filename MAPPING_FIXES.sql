-- MAPPING_FIXES.sql
--
-- Fixes to the mapping configs, applied as data changes to the rule tables that
-- `recon load-config` fills verbatim from:
--   data/amazon_payment_configs_au_old.csv  -> payment_mapping_rules
--   data/amazon_settlement_configs_au.csv   -> settlement_mapping_rules
--
-- Rows are targeted by their natural key (transaction_type / description or
-- amount_type / amount_description / amount_field), never by surrogate id alone.
-- src_line is the line of the rule in the original CSV; rules INSERTed here have
-- src_line NULL.
--
-- Apply with `make after` (which reloads the original configs first, so this
-- script always runs against the configs as provided), or manually:
--   recon load-config --payments-config ... --settlement-config ...
--   recon apply-sql --file MAPPING_FIXES.sql
--
-- One commented block per defect: what the rule was, what it is changed to,
-- why, and the variance it closes. Defects that cannot be fixed in the config
-- (or that belong in matching logic) get a commented block too.

BEGIN;

COMMIT;
