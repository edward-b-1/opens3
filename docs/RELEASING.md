# Releasing OpenS3

How a maintainer cuts a release, what the release workflow does, how anyone
verifies the result, and how to withdraw a bad release. The promises being
kept are in `GOVERNANCE.md` section 4: reproducible builds, Sigstore
signatures, an SBOM.

## Tooling

- `.goreleaser.yaml` — GoReleaser v2 configuration: builds, archives,
  checksums, SBOMs, signatures, container image, changelog, release notes.
- `Dockerfile.goreleaser` — packages the released binary into the
  distroless image (same layout as `Dockerfile`, which `make docker` uses
  to build from source).
- `.github/workflows/release.yml` — runs on every `v*` tag.
- `make release-check` validates the configuration; `make release-snapshot`
  builds `dist/` locally. Both run GoReleaser with `go run`, so nothing is
  installed; the version is pinned by `GORELEASER_VERSION` in the
  Makefile. Add `RELEASE_DOCKER=1` to also build the container images
  (needs a buildx builder that can build `linux/arm64`).

Pinned versions (update deliberately, all in one commit): GoReleaser
`v2.18.1` (Makefile; the workflow uses `~> v2`), cosign `v3.1.3`, syft
`v1.51.1`, and the GitHub Actions by commit SHA in `release.yml`.

## Cutting a release

1. Start from a clean `main` with `make ci` green and, for a minor
   release, `make conformance`, `make awscli`, `make console-test`,
   `make rotation-test` and `make migration-test` run
   so `docs/CONFORMANCE.md` and `docs/AWSCLI.md` describe this version
   (`go run ./tools/apicoverage` for `docs/API-COVERAGE.md`).
2. Update `CHANGELOG.md`: move the `[Unreleased]` entries into a new
   `## [X.Y.Z] - YYYY-MM-DD` section, add the compare links at the bottom,
   and, before v1.0, say explicitly if the on-disk format or API changed.
   Keep `docs/MANUAL.md` in step (`make manual`).
3. Commit (`Release vX.Y.Z`), then `make release-check` and
   `make release-snapshot`; run `dist/opens3_linux_amd64_v1/opens3 version`
   and start it once as a smoke test.
4. Tag and push:

   ```sh
   git tag -a vX.Y.Z -m "OpenS3 vX.Y.Z"
   git push origin main
   git push origin vX.Y.Z
   ```

   Tags are annotated; GoReleaser uses the tag for the version (`.Version`
   is the tag without the `v`, so the image is `ghcr.io/edward-b-1/opens3:X.Y.Z`).
5. Watch the `release` workflow. When it finishes, check the GitHub
   release page (assets and notes), pull the image, and run the
   verification below on a machine that is not the build machine.
6. Mirror the tag to GitLab (`git push gitlab vX.Y.Z`).

## What the workflow does

`release.yml` runs with `contents: write` (release and assets),
`packages: write` (ghcr.io) and `id-token: write` (the OIDC token that is
the keyless signing identity; no signing key exists anywhere). Steps:

1. Checkout with full history, Go from `go.mod`, **`make ci`** as the gate:
   a failing test stops the release before anything is built.
2. Install cosign and syft at pinned versions; log in to ghcr.io with the
   job's `GITHUB_TOKEN`; set up QEMU and buildx.
3. `goreleaser release --clean`:
   - `go mod verify`, then cross-compile `cmd/opens3` with `CGO_ENABLED=0
     -trimpath -ldflags "-s -w -X main.version=X.Y.Z"` for linux and darwin
     on amd64 and arm64, plus linux/armv7. The embedded modification time
     is the commit's, so builds are reproducible (see below).
   - tar.gz archives `opens3_X.Y.Z_<os>_<arch>.tar.gz` with `LICENSE`,
     `README.md` and `docs/MANUAL.md`; `checksums.txt` (SHA-256) over the
     archives and SBOMs; one SPDX JSON SBOM per archive from syft.
   - `cosign sign-blob --yes --bundle checksums.txt.sigstore.json checksums.txt`:
     the Fulcio certificate binds the signature to the workflow identity
     `https://github.com/edward-b-1/opens3/.github/workflows/release.yml@refs/tags/vX.Y.Z`,
     and the signature is recorded in the Rekor transparency log. The
     bundle holds signature, certificate and log entry.
   - buildx builds the multi-platform image from `Dockerfile.goreleaser`
     (linux/amd64 and linux/arm64, with a buildx SBOM attestation), pushes
     `ghcr.io/edward-b-1/opens3:X.Y.Z` and `:latest`, and `cosign sign
     --yes <image>@<digest>` signs the pushed manifest keylessly.
   - Publishes the GitHub release: notes header (version, install and
     verify commands, links to the reports for this tag, the pre-1.0
     stability note), then the commit list grouped into Fixes, Changes,
     Documentation and Tests, then a link to `CHANGELOG.md`.

Nothing in the workflow uses a repository secret.

## Verifying a release

```sh
V=X.Y.Z
B=https://github.com/edward-b-1/opens3/releases/download/v$V
curl -sSfLO $B/checksums.txt
curl -sSfLO $B/checksums.txt.sigstore.json
curl -sSfLO $B/opens3_${V}_linux_amd64.tar.gz
curl -sSfLO $B/opens3_${V}_linux_amd64.tar.gz.sbom.spdx.json

# 1. The checksums file was signed by this repository's release workflow
#    for this tag (cosign v3; the certificate identity is exact).
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/edward-b-1/opens3/.github/workflows/release.yml@refs/tags/v$V \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2. The archive and its SBOM match the signed checksums.
sha256sum --ignore-missing -c checksums.txt

# 3. The container image is signed by the same identity.
cosign verify ghcr.io/edward-b-1/opens3:$V \
  --certificate-identity https://github.com/edward-b-1/opens3/.github/workflows/release.yml@refs/tags/v$V \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Reproducing the build: check out the tag, run `make release-snapshot`,
and compare `sha256sum dist/*/opens3` with the binaries extracted from the
release archives. The snapshot binary reports a snapshot version string
(`-X main.version` differs), so compare a release-mode build instead when
the bytes must match exactly:

```sh
git checkout vX.Y.Z
go run github.com/goreleaser/goreleaser/v2@v2.18.1 release --skip=publish,sign,sbom,docker,validate --clean
sha256sum dist/opens3_linux_amd64_v1/opens3
```

The same Go toolchain version (from `go.mod`) is required; the binary
records it (`opens3 version`).

## Yanking a release

A release that must not be used (a security fix in flight, a corrupt
build):

1. On the GitHub release, edit the notes with a "Withdrawn: ..." line at
   the top and, if the assets are harmful, delete them or delete the
   release. Deleting the tag (`git push --delete origin vX.Y.Z`) is
   allowed only for a release that was never announced; otherwise keep it
   so the signature and Rekor entry stay auditable.
2. Container image: `gh api -X DELETE /user/packages/container/opens3/versions/<id>`
   removes the version from ghcr.io, and retag `latest` to the previous
   good release (`docker buildx imagetools create -t ghcr.io/edward-b-1/opens3:latest ghcr.io/edward-b-1/opens3:<good>`).
3. Go module proxy: caches are permanent; add a `retract` directive to
   `go.mod` (`retract vX.Y.Z // reason`) in the next release so
   `go install ...@latest` skips it.
4. Ship the fixed version as a new patch release; note the withdrawal in
   `CHANGELOG.md` and, for a security issue, follow `SECURITY.md`.
