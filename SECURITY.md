# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private
vulnerability reporting: https://github.com/edward-b-1/OpenS3/security/advisories/new
You will receive an acknowledgement within three working days and a fix
timeline within ten.

Do not report security issues in public issues or pull requests.

## What happens next

1. The report is triaged and reproduced.
2. A fix is developed on a private branch and backported to every supported
   release line.
3. A patch release is published together with an advisory describing the
   affected versions, impact, fix and any mitigation. A CVE is requested
   for anything that affects confidentiality, integrity or availability.
4. The reporter is credited unless they ask not to be.

## Supported versions

Each minor release receives security fixes for at least twelve months after
the next minor release ships (see GOVERNANCE.md, section 3).

## Design commitments

- No default credentials: the server refuses to start without operator
  supplied root credentials.
- No hard-coded tokens for internal or administrative endpoints.
- All administrative APIs require Signature V4 authentication.
- Releases are built reproducibly and signed; see GOVERNANCE.md, section 4.
