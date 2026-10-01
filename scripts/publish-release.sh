#!/usr/bin/env bash
# Builds, signs and publishes a release from a tagged commit. Run by the maintainer, on their own machine.
#   scripts/publish-release.sh v0.1.0
set -euo pipefail

TAG=${1:?usage: scripts/publish-release.sh vX.Y.Z}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

[ -z "$(git status --porcelain)" ] || { echo "the working tree has changes; release from a clean checkout" >&2; exit 1; }
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || { echo "tag $TAG does not exist; create it with: git tag -s $TAG" >&2; exit 1; }
[ "$(git rev-parse HEAD)" = "$(git rev-parse "$TAG^{commit}")" ] || { echo "HEAD is not the commit $TAG points at; check out the tag first" >&2; exit 1; }

go test ./...
scripts/build-release.sh dist/release
gh release create "$TAG" dist/release/* --title "$TAG" --generate-notes
echo "published $TAG"
