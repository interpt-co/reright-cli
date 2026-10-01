#!/usr/bin/env bash
# Builds the release binaries for every supported system and signs the checksums.
# The signing key comes from RERIGHT_RELEASE_KEY and is held by the maintainer. CI never sees it.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${1:-$ROOT/dist/release}

if [ -z "${RERIGHT_RELEASE_KEY:-}" ]; then
  echo "RERIGHT_RELEASE_KEY is not set. It holds the base64 ed25519 release key, kept by the maintainer and never committed." >&2
  echo "For a test build, make a throwaway key with: go run ./cmd/reright-release keygen" >&2
  exit 1
fi

cd "$ROOT"
PUB=$(go run ./cmd/reright-release pubkey)
rm -rf "$OUT"
mkdir -p "$OUT"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  echo "== $os/$arch"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.releasePublicKey=$PUB" -o "$OUT/reright_${os}_${arch}" ./cmd/reright
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$OUT/reright-hook_${os}_${arch}" ./cmd/reright-hook
done

go run ./cmd/reright-release sign "$OUT"
echo "== release files in $OUT"
ls -1 "$OUT"
