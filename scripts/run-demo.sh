#!/bin/sh
# Single-run RepairFlow demo. Creates a disposable workspace, serves it in the
# foreground and removes the workspace when this terminal process exits.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) arch="" ;;
esac

bin=""
for candidate in "$dir/repairflow" "$dir/../repairflow" \
  ${arch:+"$dir/repairflow-linux-$arch"}; do
  if [ -x "$candidate" ]; then
    bin=$candidate
    break
  fi
done
if [ -z "$bin" ]; then
  echo "run-demo: no executable found beside this script or in its parent" >&2
  echo "expected repairflow, repairflow-linux-amd64 or repairflow-linux-arm64" >&2
  exit 1
fi

home=$(mktemp -d "${TMPDIR:-/tmp}/repairflow-demo.XXXXXX")
server=""
cleanup() {
  if [ -n "$server" ]; then
    kill "$server" 2>/dev/null || true
    wait "$server" 2>/dev/null || true
  fi
  rm -rf "$home"
}
trap cleanup EXIT HUP INT TERM

REPAIRFLOW_NEW_PASSWORD=repairflow \
  "$bin" init --demo --demo-user demo --home "$home"

echo
echo "RepairFlow demo: http://127.0.0.1:8091/login"
echo "Username: demo"
echo "Password: repairflow"
echo "Synthetic data only. Workspace: $home"
echo "Press Ctrl+C to stop; the workspace is removed."
"$bin" serve --home "$home" &
server=$!
wait "$server"
