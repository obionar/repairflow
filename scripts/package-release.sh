#!/bin/sh
# Package signed RepairFlow binaries as installable, platform-specific archives.
#
# Release assets are intentionally limited to two archives and SHA256SUMS:
#   repairflow_Linux_x86_64.tar.gz
#   repairflow_Linux_arm64.tar.gz
#   SHA256SUMS
#
# Each archive contains only:
#   repairflow, LICENSE, THIRD_PARTY_NOTICES.md, manifest.json, manifest.sig,
#   update-github.sh
set -eu

OUT=
AMD64=
ARM64=
LICENSE=
NOTICES=
MANIFEST=
SIGNATURE=
UPDATE_SCRIPT=

usage() {
  cat <<'EOF'
Usage: scripts/package-release.sh --out DIR --amd64 PATH --arm64 PATH \
  --license PATH --notices PATH --manifest PATH --signature PATH \
  --update-script PATH

Writes repairflow_Linux_x86_64.tar.gz, repairflow_Linux_arm64.tar.gz and
SHA256SUMS into an existing empty output directory.
EOF
}

fail() {
  echo "package-release: $*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT=${2:?}; shift 2 ;;
    --amd64) AMD64=${2:?}; shift 2 ;;
    --arm64) ARM64=${2:?}; shift 2 ;;
    --license) LICENSE=${2:?}; shift 2 ;;
    --notices) NOTICES=${2:?}; shift 2 ;;
    --manifest) MANIFEST=${2:?}; shift 2 ;;
    --signature) SIGNATURE=${2:?}; shift 2 ;;
    --update-script) UPDATE_SCRIPT=${2:?}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

for value in "$OUT" "$AMD64" "$ARM64" "$LICENSE" "$NOTICES" "$MANIFEST" "$SIGNATURE" "$UPDATE_SCRIPT"; do
  [ -n "$value" ] || fail "all arguments are required"
done
[ -d "$OUT" ] || fail "output directory does not exist: $OUT"
for file in "$AMD64" "$ARM64" "$LICENSE" "$NOTICES" "$MANIFEST" "$SIGNATURE" "$UPDATE_SCRIPT"; do
  [ -f "$file" ] && [ ! -L "$file" ] || fail "input is not a regular file: $file"
done
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

STAGE=$(mktemp -d "${TMPDIR:-/tmp}/repairflow-package.XXXXXX")
trap 'rm -rf "$STAGE"' EXIT
trap 'exit 1' HUP INT TERM

stage_archive() {
  arch=$1
  label=$2
  binary=$3
  dir=$STAGE/$arch
  mkdir -p "$dir"
  install -m 0755 "$binary" "$dir/repairflow"
  install -m 0644 "$LICENSE" "$dir/LICENSE"
  install -m 0644 "$NOTICES" "$dir/THIRD_PARTY_NOTICES.md"
  install -m 0644 "$MANIFEST" "$dir/manifest.json"
  install -m 0644 "$SIGNATURE" "$dir/manifest.sig"
  install -m 0755 "$UPDATE_SCRIPT" "$dir/update-github.sh"
  archive=$STAGE/repairflow_Linux_$label.tar.gz
  tar -czf "$archive" -C "$dir" \
    repairflow LICENSE THIRD_PARTY_NOTICES.md manifest.json manifest.sig update-github.sh
  chmod 0644 "$archive"
}

stage_archive amd64 x86_64 "$AMD64"
stage_archive arm64 arm64 "$ARM64"

for archive in repairflow_Linux_x86_64.tar.gz repairflow_Linux_arm64.tar.gz; do
  [ ! -e "$OUT/$archive" ] || fail "release asset already exists: $OUT/$archive"
done
[ ! -e "$OUT/SHA256SUMS" ] || fail "release asset already exists: $OUT/SHA256SUMS"

for archive in repairflow_Linux_x86_64.tar.gz repairflow_Linux_arm64.tar.gz; do
  mv "$STAGE/$archive" "$OUT/$archive"
done
(
  cd "$OUT"
  sha256sum repairflow_Linux_x86_64.tar.gz repairflow_Linux_arm64.tar.gz > SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)
chmod 0644 "$OUT/SHA256SUMS"

count=$(wc -l < "$OUT/SHA256SUMS")
echo "package-release: PASS — 2 installable archives, $count checksums"
