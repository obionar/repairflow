#!/bin/sh
# Download and optionally apply one exact signed RepairFlow GitHub release.
# Network access occurs only when this script is invoked. No GitHub API or jq is used.
set -eu
LC_ALL=C
export LC_ALL

REPO=obionar/repairflow
TAG=
BINARY=repairflow
APPLY=0
BASE=${REPAIRFLOW_UPDATE_BASE_URL:-https://github.com}
WORK=

usage() {
  cat <<'EOF'
Usage: update-github.sh --tag VERSION [options]

  --tag VERSION       exact GitHub release tag, including prerelease tags (required)
  --repo OWNER/NAME   source repository (default: obionar/repairflow)
  --binary PATH       installed RepairFlow executable (default: repairflow on PATH)
  --apply             replace the binary after verification; default is check-only
  -h, --help          show this help

Restart the service separately after a successful apply.
EOF
}

fail() {
  echo "update-github: $*" >&2
  exit 1
}

cleanup() {
  if [ -n "$WORK" ]; then
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ $# -gt 0 ]; do
  case "$1" in
    --tag) TAG=${2:?}; shift 2 ;;
    --repo) REPO=${2:?}; shift 2 ;;
    --binary) BINARY=${2:?}; shift 2 ;;
    --apply) APPLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[ -n "$TAG" ] || fail "--tag is required"
case "$TAG" in
  -*) fail "invalid release tag" ;;
  *..*) fail "invalid release tag" ;;
  *[!0-9A-Za-z._-]*) fail "invalid release tag" ;;
esac
case "$REPO" in
  */*/*) fail "repository must have exactly the form owner/name" ;;
  */*) ;;
  *) fail "repository must have the form owner/name" ;;
esac
case "$REPO" in *[!0-9A-Za-z._/-]*) fail "invalid repository" ;; esac
REPO_OWNER=${REPO%%/*}
REPO_NAME=${REPO#*/}
case "$REPO_OWNER:$REPO_NAME" in
  -*) fail "invalid repository" ;;
  *..*) fail "invalid repository" ;;
esac
case "$BINARY" in
  */*) [ -x "$BINARY" ] || fail "binary is not executable: $BINARY" ;;
  *) BINARY=$(command -v "$BINARY") || fail "cannot find repairflow on PATH" ;;
esac
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64; LABEL=x86_64 ;;
  aarch64|arm64) ARCH=arm64; LABEL=arm64 ;;
  *) fail "unsupported architecture: $ARCH" ;;
esac
ASSET=repairflow_Linux_$LABEL.tar.gz
WORK=$(mktemp -d "${TMPDIR:-/tmp}/repairflow-update.XXXXXX")

download() {
  url=$1
  dest=$2
  echo "update-github: downloading ${url##*/}"
  (
    # POSIX file-size limit: 524288 * 512-byte blocks = 256 MiB.
    # shellcheck disable=SC3045  # BusyBox ash/dash support -c; absent = no core cap.
    ulimit -c 0 2>/dev/null || true
    ulimit -f 524288
    if command -v wget >/dev/null 2>&1; then
      wget -q -T 30 -t 2 -O "$dest" "$url"
    elif command -v curl >/dev/null 2>&1; then
      curl -fsSL --connect-timeout 10 --max-time 300 -o "$dest" "$url"
    else
      fail "wget or curl is required"
    fi
  ) || fail "download failed: $url"
  [ -s "$dest" ] || fail "downloaded file is empty: ${url##*/}"
}

BASE=${BASE%/}
RELEASE_URL=$BASE/$REPO/releases/download/$TAG
ARCHIVE=$WORK/$ASSET
download "$RELEASE_URL/$ASSET" "$ARCHIVE"
[ -s "$ARCHIVE" ] || fail "release archive is empty: $ASSET"

# Inspect before extraction: require the signed update files and reject unsafe paths.
tar -tzf "$ARCHIVE" > "$WORK/members.txt" || fail "invalid release archive: $ASSET"
for member in repairflow manifest.json manifest.sig; do
  grep -qx -- "$member" "$WORK/members.txt" || fail "release archive is missing $member"
done
if grep -Eq '(^/|(^|/)\.\.(/|$))' "$WORK/members.txt"; then
  fail "release archive contains an unsafe path"
fi
tar -xzf "$ARCHIVE" -C "$WORK" repairflow manifest.json manifest.sig ||
  fail "cannot extract release archive"
for member in repairflow manifest.json manifest.sig; do
  [ -f "$WORK/$member" ] && [ ! -L "$WORK/$member" ] || fail "$member is not a regular file"
done

"$BINARY" update check --manifest "$WORK/manifest.json" --signature "$WORK/manifest.sig"

# Bind hash/size to the artifact selected by update check, independent of the
# manifest's JSON whitespace/field formatting.
CHECK_JSON=$("$BINARY" update check --manifest "$WORK/manifest.json" --signature "$WORK/manifest.sig" --json | tr -d '[:space:]')
EXPECTED_HASH=$(printf '%s' "$CHECK_JSON" | sed -n 's/.*"artifact":{[^}]*"sha256":"\([0-9a-f]\{64\}\)".*/\1/p')
EXPECTED_SIZE=$(printf '%s' "$CHECK_JSON" | sed -n 's/.*"artifact":{[^}]*"size_bytes":\([0-9][0-9]*\).*/\1/p')
[ -n "$EXPECTED_HASH" ] && [ -n "$EXPECTED_SIZE" ] || fail "cannot read verified artifact metadata"

HASH=$(sha256sum "$WORK/repairflow" | cut -d' ' -f1)
SIZE=$(wc -c < "$WORK/repairflow" | tr -d '[:space:]')
[ "$HASH" = "$EXPECTED_HASH" ] || fail "artifact hash does not match the signed manifest"
[ "$SIZE" = "$EXPECTED_SIZE" ] || fail "artifact size does not match the signed manifest"

echo "update verified: $TAG (linux/$ARCH)"
if [ "$APPLY" -eq 1 ]; then
  "$BINARY" update apply --file "$WORK/repairflow" --manifest "$WORK/manifest.json" \
    --signature "$WORK/manifest.sig" --confirm
  echo "restart the RepairFlow service, then run version and doctor"
else
  echo "check-only: no installed file was changed; re-run with --apply to install"
fi
