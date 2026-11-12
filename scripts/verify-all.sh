#!/usr/bin/env bash
# RepairFlow Go — one-command verification gate.
#
# Runs the full check ladder in a fixed order, stops at the first failure and
# prints a per-step timing summary. Read-only with respect to user data: the
# Python smoke scripts run in disposable $TMPDIR workspaces that are removed on
# exit. No network steps are performed.
#
# Usage: scripts/verify-all.sh [--with-fuzz]
#   --with-fuzz   also run the bounded fuzz targets (`make fuzz`)
set -uo pipefail

WITH_FUZZ=0
usage() {
  cat <<'EOF'
Usage: scripts/verify-all.sh [--with-fuzz]

Runs, in order: gofmt -l, go vet, go test, go test -race, make build,
make cross, staticcheck, govulncheck, go mod verify, go mod tidy -diff,
license inventory/notices, make symbols (deterministic dist/SYMBOLS.md),
tests/e2e, the Python smoke scripts, and optionally the fuzz targets.
Stops at the first failure.

  --with-fuzz   also run `make fuzz` (off by default)
  -h, --help    show this help
EOF
}

for arg in "$@"; do
  case "$arg" in
    --with-fuzz) WITH_FUZZ=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "verify-all: unknown argument: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.." || { echo "verify-all: cannot enter repository root" >&2; exit 1; }
REPO=$PWD
BIN=$REPO/repairflow
TMPWORK=
cleanup() {
  if [ -n "$TMPWORK" ] && [ -d "$TMPWORK" ]; then
    rm -rf "$TMPWORK"
  fi
}
trap cleanup EXIT

MISSING=
for tool in go make gofmt python3 go-licenses; do
  command -v "$tool" >/dev/null 2>&1 || MISSING="$MISSING $tool"
done
if [ -n "$MISSING" ]; then
  echo "verify-all: missing required tool(s):$MISSING" >&2
  exit 1
fi

STEP_NAMES=()
STEP_TIMING=()
STEP_STATUS=()
FAILED_STEP=
CURRENT_START=0
STEP_NO=0
TOTAL=14
[ "$WITH_FUZZ" -eq 1 ] && TOTAL=15
START_ALL=$(date +%s%N)

elapsed_ms() {
  # elapsed_ms <start-ns>
  local start=$1
  local end
  end=$(date +%s%N)
  echo $(( (end - start) / 1000000 ))
}

print_summary() {
  local i total_ms=0
  echo
  echo "---- verify-all timing summary ----"
  printf '%-3s %-28s %-6s %10s\n' "#" "step" "result" "time"
  for i in "${!STEP_NAMES[@]}"; do
    printf '%-3s %-28s %-6s %9sms\n' "$((i + 1))" "${STEP_NAMES[$i]}" "${STEP_STATUS[$i]}" "${STEP_TIMING[$i]}"
    total_ms=$(( total_ms + STEP_TIMING[i] ))
  done
  printf '%-3s %-28s %-6s %9sms\n' "all" "wall clock" \
    "$([ -z "$FAILED_STEP" ] && echo PASS || echo FAIL)" \
    "$(elapsed_ms "$START_ALL")"
  echo "accounted: ${total_ms}ms"
}

run_step() {
  local name=$1
  shift
  STEP_NO=$((STEP_NO + 1))
  printf '\n==> [%d/%d] %s\n' "$STEP_NO" "$TOTAL" "$name"
  CURRENT_START=$(date +%s%N)
  if "$@"; then
    STEP_NAMES+=("$name")
    STEP_TIMING+=("$(elapsed_ms "$CURRENT_START")")
    STEP_STATUS+=(ok)
    return 0
  fi
  STEP_NAMES+=("$name")
  STEP_TIMING+=("$(elapsed_ms "$CURRENT_START")")
  STEP_STATUS+=(FAIL)
  FAILED_STEP=$name
  print_summary
  echo >&2
  echo "verify-all: FAILED at step '${name}' (step ${STEP_NO}/${TOTAL}); later steps were not run." >&2
  exit 1
}

step_fmt() {
  local out
  out=$(gofmt -l . 2>&1)
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "$out"
    return "$rc"
  fi
  if [ -n "$out" ]; then
    echo "gofmt needed:"
    echo "$out"
    return 1
  fi
  echo "gofmt: clean"
}

step_vet() { go vet ./...; }

step_test() { go test -count=1 ./...; }

step_test_race() { go test -race -count=1 ./...; }

step_build() { make build; }

step_cross() { make cross; }

step_staticcheck() { staticcheck ./...; }

step_govulncheck() { govulncheck ./...; }

step_mod_verify() { go mod verify; }

step_mod_tidy() { go mod tidy -diff; }

step_license_inventory() {
  python3 -m unittest -v tests/license_inventory_test.py
  python3 scripts/license_inventory.py check
}

step_symbols() {
  local before
  before=$(mktemp) || return 1
  if ! make symbols; then
    rm -f "$before"
    return 1
  fi
  if ! cp dist/SYMBOLS.md "$before"; then
    rm -f "$before"
    return 1
  fi
  if ! make symbols; then
    rm -f "$before"
    return 1
  fi
  if ! diff -u "$before" dist/SYMBOLS.md; then
    echo "dist/SYMBOLS.md is non-deterministic: a second 'make symbols' changed it." >&2
    rm -f "$before"
    return 1
  fi
  rm -f "$before"
  echo "dist/SYMBOLS.md reproduced unchanged"
}

step_e2e() {
  ( cd tests/e2e && go test -count=1 . )
}

step_smoke() {
  if [ ! -x "$BIN" ]; then
    echo "built binary not found at $BIN (make build must run first)" >&2
    return 1
  fi
  TMPWORK=$(mktemp -d "${TMPDIR:-/tmp}/repairflow-verify.XXXXXX") || return 1
  echo "disposable workspace root: $TMPWORK"
  if ! python3 tests/synthetic_lifecycle.py "$BIN" "$TMPWORK/lifecycle"; then
    return 1
  fi
  if ! python3 tests/hostile_cli.py "$BIN" "$TMPWORK/hostile"; then
    return 1
  fi
  echo "both smoke scripts passed"
}

step_fuzz() { make fuzz; }

echo "verify-all: repo $REPO"
echo "verify-all: fuzz $([ "$WITH_FUZZ" -eq 1 ] && echo enabled || echo disabled)"

run_step "gofmt -l" step_fmt
run_step "go vet" step_vet
run_step "go test" step_test
run_step "go test -race" step_test_race
run_step "make build" step_build
run_step "make cross" step_cross
run_step "staticcheck" step_staticcheck
run_step "govulncheck" step_govulncheck
run_step "go mod verify" step_mod_verify
run_step "go mod tidy -diff" step_mod_tidy
run_step "license inventory" step_license_inventory
run_step "make symbols" step_symbols
run_step "tests/e2e" step_e2e
run_step "python smoke scripts" step_smoke
if [ "$WITH_FUZZ" -eq 1 ]; then
  run_step "make fuzz" step_fuzz
fi

print_summary
echo
echo "verify-all: PASS"
