# End-to-end flow: make db-up (optional) -> make before -> make after -> make dump
-include .env
export

BIN      := bin/recon
DATA     := data
PAY      := $(DATA)/amazon_payments_data.csv
SET      := $(DATA)/amazon_settlements_data.txt
PAY_CFG  := $(DATA)/amazon_payment_configs_au_old.csv
SET_CFG  := $(DATA)/amazon_settlement_configs_au.csv
FIXES    := MAPPING_FIXES.sql
RECON_SCHEMA ?= recon

.PHONY: build test test-integration lint db-up db-down before after dump diagnose clean

build:
	go build -o $(BIN) ./cmd/recon

test:
	go test ./...

# needs TEST_DATABASE_URL (see .env.example); creates and drops a throwaway schema
test-integration:
	go test -count=1 -v ./internal/pipeline/

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt: files need formatting" && exit 1)
	go vet ./...

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

# Configs exactly as provided -> reports/report_before_fix.xlsx
before: build
	$(BIN) load-config --payments-config $(PAY_CFG) --settlement-config $(SET_CFG)
	-$(BIN) run --payments $(PAY) --settlements $(SET) --label before-fix --out reports/report_before_fix.xlsx

# Configs as provided + MAPPING_FIXES.sql applied to the rule tables -> reports/report_after_fix.xlsx
after: build
	$(BIN) load-config --payments-config $(PAY_CFG) --settlement-config $(SET_CFG)
	$(BIN) apply-sql --file $(FIXES)
	$(BIN) run --payments $(PAY) --settlements $(SET) --label after-fix --out reports/report_after_fix.xlsx

dump:
	mkdir -p db
	pg_dump "$(DATABASE_URL)" --schema=$(RECON_SCHEMA) --no-owner --no-privileges | gzip -9 > db/recon_dump.sql.gz
	@ls -lh db/recon_dump.sql.gz

diagnose: build
	$(BIN) diagnose

clean:
	rm -rf bin
