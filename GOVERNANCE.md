# OpenS3 Governance and Trust Charter

These commitments exist because the projects OpenS3 replaces lost their
users' trust. They are kept in the repository so that any change to them is
a visible commit.

## 1. Licence

OpenS3 is licensed under the GNU Affero General Public License, version 3
or later (AGPL-3.0-or-later), and will remain so. Releases 0.1.0 to 0.2.1
were published under the Apache License 2.0 and remain available under
it; the change was made in the open, before the first outside
contribution, so that no contributor's consent was needed and none was
bypassed (commit history: "Licence: AGPL-3.0-or-later").

The choice: the AGPL lets anyone use, modify, sell or host OpenS3, and
requires that modifications, including those run as a hosted service, be
published under the same licence. It is the licence MinIO used for its
last five years of open development, so it asks nothing new of this
project's audience, and it is the strongest guarantee this project can
give that no company can take the server private.

Contributions are accepted under the Developer Certificate of Origin
(`Signed-off-by:` on each commit, see CONTRIBUTING.md). There is no
Contributor Licence Agreement and no copyright assignment. Because every
contributor retains copyright, the project cannot be relicensed again
without the consent of every contributor.

The name OpenS3 is a trademark of the project and is governed by
`TRADEMARKS.md`, not by the licence: the code may be forked freely, but a
fork must not present itself as the project.

## 2. No open core

Every feature the server has is in the free, open-source build. There is
no enterprise edition, no licence key, no feature flag gated on payment, and
no telemetry. If a commercial offering ever exists it may sell support,
hosting or hardware, never features.

## 3. Boring releases

- A `stable` branch receives only security and bug fixes.
- Feature releases are cut from `main` no more often than every three
  months; patch releases whenever a fix warrants it.
- Each minor release is supported with security fixes for at least twelve
  months after the next minor release.
- On-disk format changes are versioned, documented in `docs/FORMAT.md`, and
  always readable by the release that introduced them and the two that
  follow. Downgrade paths are documented before a format change ships.

## 4. Security

See `SECURITY.md`. Every security fix gets a published advisory with a
CVE where applicable. Release binaries are built reproducibly with
`-trimpath`, signed with Sigstore, and shipped with an SBOM. No build ever
contains a default credential, a hard-coded token or a debug backdoor; the
root credentials must be supplied by the operator.

## 5. Transparency of data

The on-disk layout of metadata and objects is documented in `docs/FORMAT.md`
and can be read and recovered with ordinary tools. A user must always be able
to get their bytes back without OpenS3 running.

## 6. Governance

The project is currently maintained by its founder. When there are regular
contributors from at least two other organisations, maintainership moves to
a steering group elected by contributors, and the project will seek a home
in a neutral foundation. The trademark, if registered, will be held so that
forks may not be prevented from describing themselves as derived from OpenS3.

## 7. Changing this document

Changes to sections 1, 2 and 5 require a release announcement at least
ninety days before they take effect.
