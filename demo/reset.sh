#!/bin/sh
# Restore a persistent demo workspace to pristine synthetic data. Refuses if
# the workspace is not the unchanged demo dataset; users and sessions survive.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/.." && pwd)
. "$dir/bootstrap.sh"

home="$dir/workspace"
if [ ! -f "$home/config.toml" ]; then
  echo "reset-demo: no workspace at $home; run dev.sh first" >&2
  exit 1
fi
bin=$(find_or_fetch_binary "$root" "$dir")

"$bin" demo reset --confirm --home "$home"
