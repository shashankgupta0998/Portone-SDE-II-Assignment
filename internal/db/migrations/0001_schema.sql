-- Core schema. All objects are created unqualified; the connection's search_path
-- points at RECON_SCHEMA (default "recon").

-- ---------------------------------------------------------------------------
-- Ingestion: one row per ingested file, and ONE table for the rows of both files.
-- ---------------------------------------------------------------------------
CREATE TABLE ingest_files (
    id            bigserial PRIMARY KEY,
    source        text        NOT NULL CHECK (source IN ('payments', 'settlements')),
    file_name     text        NOT NULL,           -- basename as provided
    sha256        text        NOT NULL,
    size_bytes    bigint      NOT NULL,
    data_rows     int,
    skipped_lines int,
    error_rows    int,
    ingested_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, file_name)                    -- re-ingesting a file replaces it
);

CREATE TABLE source_rows (
    id                bigserial PRIMARY KEY,
    file_id           bigint  NOT NULL REFERENCES ingest_files (id) ON DELETE CASCADE,
    source            text    NOT NULL CHECK (source IN ('payments', 'settlements')),
    line_no           int     NOT NULL,           -- physical 1-based line in the original file
    row_kind          text    NOT NULL CHECK (row_kind IN ('data', 'settlement_header', 'error')),

    -- common normalised attributes (NULL = not applicable to this source)
    settlement_id     text,
    transaction_type  text,                       -- payments "type" / settlements "transaction-type" (raw value)
    amount_type       text,                       -- settlements "amount-type"
    description       text,                       -- payments "description" / settlements "amount-description"
    order_id          text,
    merchant_order_id text,
    adjustment_id     text,
    shipment_id       text,
    sku               text,
    quantity          int,
    marketplace       text,
    fulfillment       text,
    posted_at         timestamptz,                -- payments date/time / settlements posted-date-time
    release_at        timestamptz,                -- payments Transaction Release Date
    txn_status        text,                       -- payments Transaction status (Released / Deferred)
    ref_date          date,                       -- value of the record_ref "date" token

    -- amounts
    amount                 numeric(18, 2),        -- settlements amount
    product_sales          numeric(18, 2),        -- payments typed amount columns ...
    shipping_credits       numeric(18, 2),
    gift_wrap_credits      numeric(18, 2),
    promotional_rebates    numeric(18, 2),
    sales_tax_collected    numeric(18, 2),
    low_value_goods        numeric(18, 2),
    selling_fees           numeric(18, 2),
    fba_fees               numeric(18, 2),
    other_transaction_fees numeric(18, 2),
    other                  numeric(18, 2),
    total                  numeric(18, 2),

    -- settlement header row fields
    settlement_start  timestamptz,
    settlement_end    timestamptz,
    deposit_date      timestamptz,
    total_amount      numeric(18, 2),
    currency          text,

    -- lineage
    raw               jsonb   NOT NULL,           -- original header -> original cell text
    raw_line          text    NOT NULL,           -- exact original line text
    UNIQUE (file_id, line_no)
);
CREATE INDEX source_rows_source_settlement_idx ON source_rows (source, settlement_id);
CREATE INDEX source_rows_order_idx ON source_rows (order_id);
CREATE INDEX source_rows_sku_idx ON source_rows (sku);

CREATE TABLE ingest_errors (
    id        bigserial PRIMARY KEY,
    file_id   bigint NOT NULL REFERENCES ingest_files (id) ON DELETE CASCADE,
    line_no   int    NOT NULL,
    severity  text   NOT NULL CHECK (severity IN ('info', 'error')),
    field     text   NOT NULL DEFAULT '',
    raw_value text   NOT NULL DEFAULT '',
    message   text   NOT NULL
);

-- ---------------------------------------------------------------------------
-- Mapping configuration: loaded verbatim from the CSVs, fixed only by SQL
-- (MAPPING_FIXES.sql). src_line is NULL for rules inserted by a fix.
-- ---------------------------------------------------------------------------
CREATE TABLE payment_mapping_rules (
    id                                    serial PRIMARY KEY,
    src_line                              int,
    transaction_type                      text NOT NULL DEFAULT '',
    description                           text NOT NULL,
    amount_field                          text NOT NULL,
    record_ref                            text NOT NULL,
    to_summary_field_when_positive_amount text NOT NULL DEFAULT '',
    to_summary_field_when_negative_amount text NOT NULL DEFAULT ''
);

CREATE TABLE settlement_mapping_rules (
    id                                    serial PRIMARY KEY,
    src_line                              int,
    transaction_type                      text NOT NULL DEFAULT '',
    amount_type                           text NOT NULL,
    amount_description                    text NOT NULL,
    record_ref                            text NOT NULL,
    to_summary_field_when_positive_amount text NOT NULL DEFAULT '',
    to_summary_field_when_negative_amount text NOT NULL DEFAULT ''
);

-- Summary sheet layout as data: which summary_field feeds which line.
CREATE TABLE summary_lines (
    summary_field      text PRIMARY KEY,
    section            text NOT NULL CHECK (section IN ('Sales', 'Refunds', 'Expenses', 'Paid To Amazon')),
    label              text NOT NULL,             -- Summary sheet label
    consolidated_label text NOT NULL,             -- Consolidated Data column label
    sort_order         int  NOT NULL UNIQUE
);

-- ---------------------------------------------------------------------------
-- Derived data. Only the latest run is kept in full; runs and
-- run_history_summary keep per-run totals so before/after deltas survive.
-- ---------------------------------------------------------------------------
CREATE TABLE runs (
    id                      bigserial PRIMARY KEY,
    label                   text        NOT NULL,
    started_at              timestamptz NOT NULL DEFAULT now(),
    finished_at             timestamptz,
    scope_settlement_ids    text[],
    payment_rules_sha256    text,
    settlement_rules_sha256 text,
    reconciled              int,
    unreconciled_payment    int,
    unreconciled_settlement int,
    out_of_scope_payment    int,
    report_path             text
);

CREATE TABLE mapped_components (
    id            bigserial PRIMARY KEY,
    run_id        bigint   NOT NULL REFERENCES runs (id),
    source_row_id bigint   NOT NULL REFERENCES source_rows (id) ON DELETE CASCADE,
    source        text     NOT NULL,
    rule_id       int,                            -- NULL when no rule matched
    rule_level    smallint,                       -- precedence level that matched (1..4)
    amount_field  text     NOT NULL,              -- payments column name, or 'amount' for settlements
    amount        numeric(18, 2) NOT NULL,
    summary_field text     NOT NULL DEFAULT '',   -- '' = not summarised
    record_ref    text,                           -- NULL when unmapped / unruled
    status        text     NOT NULL CHECK (status IN ('routed', 'not_summarised', 'zero', 'unmapped', 'unruled_column')),
    empty_tokens  text                            -- record_ref field tokens that resolved empty
);
CREATE INDEX mapped_components_row_idx ON mapped_components (source_row_id);
CREATE INDEX mapped_components_ref_idx ON mapped_components (record_ref);
CREATE INDEX mapped_components_rule_idx ON mapped_components (source, rule_id);

CREATE TABLE stream_summary (
    run_id        bigint NOT NULL REFERENCES runs (id),
    source        text   NOT NULL,
    settlement_id text   NOT NULL,
    txn_status    text   NOT NULL,
    summary_field text   NOT NULL,
    amount        numeric(18, 2) NOT NULL,
    components    int    NOT NULL,
    PRIMARY KEY (run_id, source, settlement_id, txn_status, summary_field)
);

CREATE TABLE recon_records (
    run_id            bigint NOT NULL REFERENCES runs (id),
    record_ref        text   NOT NULL,
    status            text   NOT NULL CHECK (status IN ('reconciled', 'unreconciled_payment', 'unreconciled_settlement', 'out_of_scope_payment')),
    pay_rows          int    NOT NULL DEFAULT 0,
    set_rows          int    NOT NULL DEFAULT 0,
    pay_total         numeric(18, 2),
    set_total         numeric(18, 2),
    diff_total        numeric(18, 2) NOT NULL,
    pay_first_row_id  bigint,
    set_first_row_id  bigint,
    pay_lines         int[],
    set_lines         int[],
    pay_rule_ids      int[],
    set_rule_ids      int[],
    set_amount_types  text,
    PRIMARY KEY (run_id, record_ref, status)
);

CREATE TABLE recon_record_buckets (
    run_id        bigint NOT NULL REFERENCES runs (id),
    record_ref    text   NOT NULL,
    side          text   NOT NULL CHECK (side IN ('payments', 'settlements', 'out_of_scope_payments')),
    summary_field text   NOT NULL,
    amount        numeric(18, 2) NOT NULL,
    PRIMARY KEY (run_id, record_ref, side, summary_field)
);

CREATE TABLE run_checks (
    run_id   bigint NOT NULL REFERENCES runs (id),
    name     text   NOT NULL,
    status   text   NOT NULL CHECK (status IN ('pass', 'fail', 'info')),
    detail   text   NOT NULL,
    PRIMARY KEY (run_id, name)
);

CREATE TABLE run_history_summary (
    run_id        bigint NOT NULL REFERENCES runs (id),
    source        text   NOT NULL,
    summary_field text   NOT NULL,
    amount        numeric(18, 2) NOT NULL,
    PRIMARY KEY (run_id, source, summary_field)
);
