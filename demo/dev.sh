#!/bin/sh
# Persistent contributor demo workspace. Seeds synthetic demo data once under
# demo/workspace (gitignored), then serves it in the foreground. Edit freely;
# use reset.sh only while the synthetic content is still pristine.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/.." && pwd)
. "$dir/bootstrap.sh"

home="$dir/workspace"
bin=$(find_or_fetch_binary "$root" "$dir")

if [ ! -f "$home/config.toml" ]; then
  REPAIRFLOW_NEW_PASSWORD=repairflow \
    "$bin" init --demo --demo-user demo --home "$home"
else
  echo "demo: reusing $home (run reset.sh to restore pristine demo data)"
fi

port=$(sed -n 's/^port = \([0-9][0-9]*\)$/\1/p' "$home/config.toml" 2>/dev/null | head -n 1)
port=${port:-8091}

echo
echo "RepairFlow contributor demo: http://127.0.0.1:$port/login"
echo "Username: demo"
echo "Password: repairflow"
echo "Synthetic data only. Workspace: $home"
echo "Press Ctrl+C to stop; the workspace is kept for the next run."

exec "$bin" serve --home "$home"
