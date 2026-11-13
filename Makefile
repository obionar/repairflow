# RepairFlow Go — build and verification entrypoints.
#
# Builds are reproducible: CGO off, -trimpath, and a build date taken from the
# git commit (not the wall clock) unless the caller overrides DATE.
# See docs/USAGE.md.

GO      ?= go
VERSION ?= 0.1.0-dev
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS := -X repairflow/internal/buildinfo.Version=$(VERSION) \
           -X repairflow/internal/buildinfo.Commit=$(COMMIT) \
           -X repairflow/internal/buildinfo.Date=$(DATE)
BIN := repairflow

.PHONY: build test vet fmt-check verify verify-all fuzz e2e cross release symbols clean licenses license-check

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/repairflow

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

verify: vet fmt-check test

# Full gate in a fixed order with a timing summary (scripts/verify-all.sh).
verify-all:
	bash scripts/verify-all.sh

# Browser-level web UI tests: Playwright + system Chromium.
# Test-only tooling lives in tests/e2e (separate module).
e2e:
	cd tests/e2e && go test -v .

fuzz:
	$(GO) test -run=NONE -fuzz=FuzzParseToml -fuzztime=25s ./internal/config/
	$(GO) test -run=NONE -fuzz=FuzzParseGlobalFlags -fuzztime=15s ./internal/cli/
	$(GO) test -run=NONE -fuzz=FuzzRun -fuzztime=25s ./internal/cli/

# Verify the compiled-dependency inventory and reviewed first-party assets.
license-check:
	python3 scripts/license_inventory.py check

# Publishable release archives (scripts/release.sh). KEY is required to embed the
# public key and sign the manifest; unsigned publishable releases are refused.
release:
	VERSION=$(VERSION) bash scripts/release.sh --version $(VERSION) $(if $(KEY),--key $(KEY),)

cross:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-linux-amd64 ./cmd/repairflow
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-linux-arm64 ./cmd/repairflow

symbols:
	mkdir -p dist
	$(GO) run ./cmd/symbols -out dist/SYMBOLS.md

clean:
	rm -rf $(BIN) dist
