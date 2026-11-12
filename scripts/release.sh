#!/usr/bin/env bash
# Build a publishable RepairFlow release directory.
#
#   scripts/release.sh --version 0.1.0 [--key PATH] [--pub PATH] [--out DIR] [--channel stable]
#
# Produces dist/<version>/ with exactly three release assets:
#   repairflow_Linux_x86_64.tar.gz
#   repairflow_Linux_arm64.tar.gz
#   SHA256SUMS
#
# Each archive contains only the binary, LICENSE, THIRD_PARTY_NOTICES.md, the
# signed manifest, manifest signature and update-github.sh. No loose binaries,
# docs, demo launcher or source archive are published as release assets.
# Refuses a dirty worktree, a -dev version and an existing output directory.
# Never prints key material.
#
# Without --key the artifacts are unsigned: `update apply` stays disabled on
# them by design (no embedded public key). With --key the public key is read
# from --pub (default: release-signing.pub next to the key), embedded in both
# binaries, and the manifest is signed.
set -euo pipefail

VERSION=""
KEY=""
PUB=""
OUT=""
CHANNEL="stable"

usage() {
  cat <<'EOF'
Usage: scripts/release.sh --version X.Y.Z [options]

  --version X.Y.Z   release version (no -dev suffix, no leading v)
  --key PATH        Ed25519 private key written by `repairflow update keygen` (required)
  --pub PATH        public key hex file (default: release-signing.pub beside --key)
  --out DIR         output directory (default: dist/<version>; must not exist)
  --channel NAME    release channel recorded in the manifest (default: stable)
  -h, --help        this help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION=${2:?}; shift 2 ;;
    --key)     KEY=${2:?}; shift 2 ;;
    --pub)     PUB=${2:?}; shift 2 ;;
    --out)     OUT=${2:?}; shift 2 ;;
    --channel) CHANNEL=${2:?}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "release: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$VERSION" ] || { echo "release: --version is required" >&2; exit 2; }
case "$VERSION" in
  *-dev*) echo "release: refusing version '$VERSION' (development builds must not be published)" >&2; exit 2 ;;
  v*) echo "release: version must not start with 'v'" >&2; exit 2 ;;
esac
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'; then
  echo "release: version '$VERSION' is not semver (X.Y.Z)" >&2; exit 2
fi

cd "$(dirname "$0")/.."
REPO=$PWD
if [ -n "$(git status --porcelain)" ]; then
  echo "release: worktree is dirty; commit or stash before releasing" >&2
  git status --short >&2
  exit 1
fi

python3 scripts/license_inventory.py check

COMMIT=$(git rev-parse --short=12 HEAD)
DATE=$(git log -1 --format=%cI)
[ -n "$OUT" ] || OUT="$REPO/dist/$VERSION"
if [ -e "$OUT" ]; then
  echo "release: output directory already exists: $OUT" >&2
  exit 1
fi
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

PUBHEX=""
if [ -n "$KEY" ]; then
  [ -f "$KEY" ] || { echo "release: key not found: $KEY" >&2; exit 1; }
  [ -n "$PUB" ] || PUB="$(dirname "$KEY")/release-signing.pub"
  [ -f "$PUB" ] || { echo "release: public key not found: $PUB (pass --pub)" >&2; exit 1; }
  PUBHEX=$(tr -d '[:space:]' < "$PUB")
  if ! printf '%s' "$PUBHEX" | grep -Eq '^[0-9a-f]{64}$'; then
    echo "release: public key must be 64 hex characters" >&2; exit 1
  fi
fi

LDFLAGS="-X repairflow/internal/buildinfo.Version=$VERSION -X repairflow/internal/buildinfo.Commit=$COMMIT -X repairflow/internal/buildinfo.Date=$DATE"
if [ -n "$PUBHEX" ]; then
  LDFLAGS="$LDFLAGS -X repairflow/internal/buildinfo.PublicKey=$PUBHEX"
fi

echo "release: building $VERSION ($COMMIT) into $OUT"
BUILD=$(mktemp -d "${TMPDIR:-/tmp}/repairflow-release.XXXXXX")
cleanup() {
  rm -rf "$BUILD"
  if [ -n "${CHECK:-}" ]; then
    rm -rf "$CHECK"
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" \
    -o "$BUILD/repairflow-$arch" ./cmd/repairflow
done

# Minimal JSON writer: no jq dependency, no shell interpolation surprises.
python3 - "$BUILD" "$VERSION" "$CHANNEL" "$DATE" <<'PY'
import hashlib, json, os, sys
build, version, channel, created = sys.argv[1:5]
artifacts = []
for arch in ("amd64", "arm64"):
    path = os.path.join(build, f"repairflow-{arch}")
    digest = hashlib.sha256(open(path, "rb").read()).hexdigest()
    artifacts.append({"os": "linux", "arch": arch, "file": "repairflow",
                      "sha256": digest, "size_bytes": os.path.getsize(path)})
manifest = {"format_version": 1, "channel": channel, "version": version,
            "created_at": created, "artifacts": artifacts}
with open(os.path.join(build, "manifest.json"), "w") as fh:
    json.dump(manifest, fh, indent=2)
    fh.write("\n")
PY

if [ -n "$KEY" ]; then
  "$BUILD/repairflow-amd64" update sign --key "$KEY" --manifest "$BUILD/manifest.json" \
    --out "$BUILD/manifest.sig" >/dev/null
  [ -s "$BUILD/manifest.sig" ] || { echo "release: signing produced no signature" >&2; exit 1; }

  # Prove the manifest is accepted by a binary that embeds the same public key.
  CHECK=$(mktemp -d)
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-X repairflow/internal/buildinfo.Version=0.0.1 -X repairflow/internal/buildinfo.PublicKey=$PUBHEX" \
    -o "$CHECK/repairflow-check" ./cmd/repairflow
  if ! "$CHECK/repairflow-check" update check --manifest "$BUILD/manifest.json" \
      --signature "$BUILD/manifest.sig" --json | grep -q "\"new_version\":\"$VERSION\""; then
    echo "release: the generated manifest was not accepted by the checker build" >&2
    exit 1
  fi
  echo "release: manifest verified against the embedded public key"
else
  echo "release: --key is required for a publishable update archive" >&2
  exit 1
fi

scripts/package-release.sh \
  --out "$OUT" \
  --amd64 "$BUILD/repairflow-amd64" \
  --arm64 "$BUILD/repairflow-arm64" \
  --license LICENSE \
  --notices THIRD_PARTY_NOTICES.md \
  --manifest "$BUILD/manifest.json" \
  --signature "$BUILD/manifest.sig" \
  --update-script scripts/update-github.sh

count=$(wc -l < "$OUT/SHA256SUMS")
echo "release: PASS — $VERSION in $OUT ($count installable archives)"
