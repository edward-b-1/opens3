# Contributing to OpenS3

## Developer Certificate of Origin

By contributing you certify the Developer Certificate of Origin
(https://developercertificate.org/): that you wrote the change or have the
right to submit it under the Apache-2.0 licence. Add a sign-off line to every
commit:

    Signed-off-by: Your Name <you@example.com>

`git commit -s` adds it for you. Commits without a sign-off are not merged.
There is no CLA and no copyright assignment.

## Before you open a merge request

- Run `make ci` (vet + race tests). It must pass.
- If you touched the S3 API, add or update a conformance test in
  `tests/integration` and, where possible, a recorded AWS exchange in
  `tests/conformance/fixtures`.
- If you touched the on-disk layout, update `docs/FORMAT.md` and bump the
  format version.
- Keep the dependency list short; prefer the standard library.

## Where things live

See `CLAUDE.md` for the build and test commands and `docs/PLAN.md` for the
architecture and roadmap.
