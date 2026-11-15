#!/bin/sh
# Public one-shot demo. Finds an existing binary, builds when possible, or
# downloads the matching Linux release into demo/bin, then delegates to the
# canonical launcher in scripts/run-demo.sh: temporary workspace, synthetic
# data, demo login, foreground server, cleanup on exit.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/.." && pwd)
. "$dir/bootstrap.sh"

bin=$(find_or_fetch_binary "$root" "$dir")
REPAIRFLOW_BIN=$bin exec "$root/scripts/run-demo.sh"
