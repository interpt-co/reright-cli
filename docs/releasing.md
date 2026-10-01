# Releasing

Releases are built and signed by the maintainer on their own machine. Agents and CI never see the signing key.

## The signing key

An ed25519 key signs `checksums.txt`. Its public half is compiled into every `reright` binary. `reright install` refuses to use a hook binary whose checksum is not in a file signed by that key. If the key is lost or leaks, every installed `reright` must be replaced by a build with a new key, so keep an offline copy.

```sh
go run ./cmd/reright-release keygen    # prints a private and a public key, both base64
```

Store the private key in a password manager. The build script derives the public key from it.

## Cut a release

```sh
git tag -s vX.Y.Z
export RERIGHT_RELEASE_KEY=<private key>
scripts/publish-release.sh vX.Y.Z
```

The script checks that the tree is clean and HEAD is the tag, runs the tests, builds `reright` and `reright-hook` for linux and darwin on amd64 and arm64 with `-trimpath`, signs `checksums.txt`, and creates a GitHub release with every file. `release.DefaultBaseURL` points at `releases/latest/download`, so the newest release is what `reright install` fetches.

## Testing without the real key

`scripts/build-release.sh` with a throwaway key from `keygen` gives binaries that trust only that key. Serve `dist/release/` from any static server and pass it to `reright install --release-url`.
