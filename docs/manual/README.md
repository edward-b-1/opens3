# OpenS3 User Manual

OpenS3 is an S3-compatible object store in a single binary. This manual is
for the people who run it and the people who use it. Design documents
(`../PLAN.md`, `../SURVEY.md`, `../FORMAT.md`) and generated reports
(`../API-COVERAGE.md`, `../CONFORMANCE.md`, `../AWSCLI.md`) are referenced
where they go deeper.

1. [Getting started](01-getting-started.md) — install, first start, first bucket
2. [Configuration](02-configuration.md) — flags, environment variables, TLS
3. [Identity and access](03-identity.md) — users, keys, passwords, groups, policies
4. [Buckets and objects](04-buckets-and-objects.md) — versioning, object lock, lifecycle, tags, CORS, public access
5. [Encryption](05-encryption.md) — the four states, master key, named keys, SSE-C
6. [The console](06-console.md) — web interface
7. [Notifications](07-notifications.md) — events to webhooks
8. [Operations](08-operations.md) — backups, restores, upgrades, monitoring, logs
9. [Compatibility](09-compatibility.md) — what works, what differs from AWS and MinIO
10. [Troubleshooting](10-troubleshooting.md) — errors you may see and what they mean
