#!/bin/sh
# Shared demo binary discovery. Sources:
#   1. REPAIRFLOW_BIN
#   2. <repo>/repairflow
#   3. <repo>/demo/bin/repairflow
#   4. repairflow on PATH
#   5. build from source when Go+make exist
#   6. download the matching Linux release archive into demo/bin
# The function prints the chosen executable path on stdout; messages go to stderr.
# Callers run with set -eu.

demo_repo=${REPAIRFLOW_DEMO_REPO:-obionar/repairflow}
demo_tag=${REPAIRFLOW_DEMO_TAG:-latest}
demo_no_download=${REPAIRFLOW_DEMO_NO_DOWNLOAD:-0}

demo_fail() {
  echo "demo: $*" >&2
  exit 1
}

demo_platform() {
  os=$(uname -s 2>/dev/null || echo unknown)
  case "$os" in
    Linux) ;;
    *) demo_fail "no published demo build for $os; release archives are Linux-only. Build from source or use a Linux host." ;;
  esac
  case "$(uname -m 2>/dev/null || true)" in
    x86_64|amd64) echo "amd64 x86_64" ;;
    aarch64|arm64) echo "arm64 arm64" ;;
    *) demo_fail "unsupported architecture $(uname -m 2>/dev/null || echo unknown); releases cover linux/amd64 and linux/arm64" ;;
  esac
}

demo_existing() {
  root=$1
  demo_dir=$2
  if [ -n "${REPAIRFLOW_BIN:-}" ]; then
    if [ -x "$REPAIRFLOW_BIN" ]; then
      printf '%s\n' "$REPAIRFLOW_BIN"
      return 0
    fi
    demo_fail "REPAIRFLOW_BIN is not executable: $REPAIRFLOW_BIN"
  fi
  for candidate in "$root/repairflow" "$demo_dir/bin/repairflow"; do
    if [ -x "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  if command -v repairflow >/dev/null 2>&1; then
    command -v repairflow
    return 0
  fi
  return 1
}

demo_build() {
  root=$1
  command -v go >/dev/null 2>&1 || return 1
  command -v make >/dev/null 2>&1 || return 1
  echo "demo: building repairflow from source..." >&2
  if ! make -C "$root" build >&2; then
    echo "demo: source build failed; trying a released binary" >&2
    return 1
  fi
  [ -x "$root/repairflow" ] || return 1
  printf '%s\n' "$root/repairflow"
}

demo_download() {
  root=$1
  demo_dir=$2
  if [ "$demo_no_download" = "1" ]; then
    demo_fail "no executable found and REPAIRFLOW_DEMO_NO_DOWNLOAD=1 is set"
  fi
  platform=$(demo_platform) || exit 1
  label=${platform##* }
  asset="repairflow_Linux_${label}.tar.gz"
  if [ "$demo_tag" = "latest" ]; then
    base="https://github.com/${demo_repo}/releases/latest/download"
  else
    case "$demo_tag" in
      -*) demo_fail "invalid REPAIRFLOW_DEMO_TAG: $demo_tag" ;;
      *..*) demo_fail "invalid REPAIRFLOW_DEMO_TAG: $demo_tag" ;;
    esac
    base="https://github.com/${demo_repo}/releases/download/${demo_tag}"
  fi
  # Local mirror/test seam: the value is the release download root.
  if [ -n "${REPAIRFLOW_DEMO_BASE_URL:-}" ]; then
    base=$REPAIRFLOW_DEMO_BASE_URL
  fi
  for tool in tar sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || demo_fail "$tool is required to download the demo binary"
  done
  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --connect-timeout 10 --max-time 300 -o "$2" "$1"; }
  elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -T 30 -t 2 -O "$2" "$1"; }
  else
    demo_fail "curl or wget is required to download the demo binary"
  fi

  mkdir -p "$demo_dir/bin"
  work=$(mktemp -d "$demo_dir/bin/.download.XXXXXX") || demo_fail "cannot create download directory"
  trap 'rm -rf "$work"' EXIT HUP INT TERM

  echo "demo: downloading $asset ($demo_tag) from the release..." >&2
  fetch "$base/$asset" "$work/$asset" || demo_fail "download failed: $base/$asset"
  fetch "$base/SHA256SUMS" "$work/SHA256SUMS" || demo_fail "download failed: $base/SHA256SUMS"
  expected=$(awk -v file="$asset" '$2 == file { print $1; exit }' "$work/SHA256SUMS")
  [ -n "$expected" ] || demo_fail "SHA256SUMS does not list $asset"
  actual=$(sha256sum "$work/$asset" | awk '{print $1}')
  [ "$actual" = "$expected" ] || demo_fail "checksum mismatch for $asset"

  tar -xzf "$work/$asset" -C "$work" repairflow || demo_fail "cannot extract repairflow from $asset"
  [ -f "$work/repairflow" ] && [ ! -L "$work/repairflow" ] || demo_fail "archive did not contain a regular repairflow binary"
  chmod 0755 "$work/repairflow"
  mv -f "$work/repairflow" "$demo_dir/bin/repairflow" || demo_fail "cannot install the downloaded demo binary"
  rm -rf "$work"
  trap - EXIT HUP INT TERM
  echo "demo: installed $demo_dir/bin/repairflow" >&2
  printf '%s\n' "$demo_dir/bin/repairflow"
}

find_or_fetch_binary() {
  root=$1
  demo_dir=$2
  if bin=$(demo_existing "$root" "$demo_dir"); then
    printf '%s\n' "$bin"
    return 0
  fi
  if bin=$(demo_build "$root"); then
    printf '%s\n' "$bin"
    return 0
  fi
  demo_download "$root" "$demo_dir"
}
