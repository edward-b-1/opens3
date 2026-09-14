# Console checks

Two Docker-tier checks for the embedded web console (`internal/console`,
static files in `internal/console/static`). Nothing in `go test` runs the
page's JavaScript, so these are the only tests that catch a helper missing
from a view's destructuring, a stale form field or an uncaught exception.
Both need Docker; nothing is installed on the host.

    make lint-js        # ESLint only, a few seconds
    make console-test   # lint-js, then the browser smoke test (about a minute)

## Lint (`lint.sh`, `eslint.config.js`)

ESLint runs in the pinned `node:22.23.2-alpine` image with the static
directory mounted read-only. Flat config with browser globals plus the
console's own (`ui`, `api`, `app`, `settings`, `views`), ecmaVersion 2022,
classic scripts. Rules: `no-undef`, `no-unused-vars` (unused arguments and
`catch (e)` bindings allowed), `no-redeclare`, `no-shadow-restricted-names`,
`no-implicit-globals` (with lexical bindings: a top-level `const` in one
script collides with the same name in another at load time), and `eqeqeq`
as a warning. Packages (`eslint@10.10.0`, `globals@17.12.0`) install once
into `.cache/eslint`; later runs are offline. `CONSOLE_LINT_FIX=1` runs
`--fix` against the static directory.

## Browser smoke test (`run.sh`, `playwright.config.js`, `smoke.spec.js`)

`run.sh` builds `opens3`, starts it on a free `127.0.0.1` port with a
temporary data root and `--no-fsync`, waits for `/opens3/health/ready` and
runs Playwright Test in `mcr.microsoft.com/playwright:v1.63.0-noble` with
`--network host`. `@playwright/test@1.63.0` installs once into
`.cache/playwright`. Results go to `results/`: `junit.xml`, `report.json`,
`playwright.log`, `server.log`, and under `artifacts/` the screenshot,
video and trace of every failed test (`npx playwright show-trace` opens a
trace).

Every test registers `pageerror` and `console` listeners before the first
navigation and asserts at the end that nothing was reported (the browser's
own "Failed to load resource: 401" for the pre-login `/console/api/me`
probe, and the deliberately failed logins, are the only allowances). The
tests cover:

- sign in as root; app shell visible, http banner hidden on localhost;
  every route (`#/buckets`, object browser, bucket settings, the four
  identity tabs, `#/kms`, `#/status`); the settings dialog via the gear and
  the `,` key with compact density and dark theme reflected in
  `data-density` / `data-theme` on `<html>`;
- bucket lifecycle: create through the dialog, upload a file through the
  Upload dialog, file icon in the row, details panel, download and compare
  the body, new folder, delete object, delete folder (dry-run
  confirmation), delete bucket;
- bucket settings: SSE-S3 default encryption, versioning Enabled, a tag,
  a public-read bucket policy (Public badge) and its removal, all checked
  again after a reload;
- identity: create a user with a console password and read the generated
  key pair (20/40 characters), create a second key, sign out, sign in as
  that user, admin routes redirect to buckets, "My access keys" lists both
  keys, the Change password dialog opens and cancels;
- login negatives: wrong password (toast "invalid user name or password"),
  an access key pair (toast mentions the S3 API).

Options: `CONSOLE_BROWSERS` selects browsers (default `all`: Chromium,
Firefox and WebKit, about ten seconds together; `chromium` for the
required one only); `CONSOLE_WORKERS` sets Playwright workers (default 4); extra Playwright arguments pass through
(`tests/console/run.sh -g identity`, or `make console-test
CONSOLE_ARGS='-g identity'`); `CONSOLE_KEEP=1` keeps the server's data dir.
