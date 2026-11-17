#!/bin/sh
# Compatibility name for the canonical GitHub update helper.
set -eu
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$dir/repairflow-update" "$@"
