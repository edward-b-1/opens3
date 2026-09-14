# MinIO web console feature audit and OpenS3 comparison

Audit date: **2026-09-14**. OpenS3 baseline:
`9291ea2596b48324809475044b2d49f2024aedd6` (clean working tree before this
documentation change).

OpenS3 already implements the core object browser, bucket creation and
versioning, bucket policies and default encryption, local IAM administration,
and named encryption keys. Its largest opportunities are richer object
operations, bucket configuration editors, and operational visibility. Several
of these need UI and console-handler work around existing server capabilities.
Replication, remote tiering, quotas, and external identity providers require
additional server functionality.

## Scope and method

“The MinIO console” is not a single stable feature set. This audit distinguishes
the following surfaces:

| Baseline | Evidence and reason for including it |
|---|---|
| Historical full console, **v1.7.3**, 2024-10-30 | Inspected the upstream source distributed in the [versioned Go module archive][H-archive], with [origin metadata][H-origin] identifying commit `fce84d1de0c99e36ca1743be599c1a1f530f77e6`. This predates the v1.7.4 removals and is the main benchmark for recovering former administration workflows. |
| Reduced community UI, May 2025 snapshot | Inspected upstream commit `220a55500cc3b1571c0d304b0f90177c531aa735`, distributed as [this module archive][R-archive] with [origin metadata][R-origin]. Its changelog describes v2.0.0's return to an object browser. This is a historical comparison snapshot, **not a claim to have tested the latest community build**. |
| AIStor's embedded Console UI | Used MinIO's current [console overview][A0], [object-management documentation][A1], [IAM documentation][A2], and [deployment-management documentation][A3], accessed on the audit date. These establish documented commercial-product workflows, not a verified entitlement list for every subscription or release. |
| AIStor Global Console | Included separately using MinIO's [Global Console product page][G1] and [AIStor announcement][G2]. This manages multiple deployments and additional products; it is a broader target than OpenS3's embedded console. |

The source archives were useful because direct requests to some original
MinIO Console GitHub source and PR URLs returned errors during the audit.
They preserve the actual upstream code, including UI routes, forms, action
menus, and feature gates; the source register below gives paths within them.

For OpenS3, the audit follows the [browser routes][app], each view, the
[console API route table][routes], relevant server implementations, and the
[existing browser test suite][smoke]. A documented API operation or a database
field alone does not establish a working feature. In particular, replication
configuration storage and storage-class labels are not counted as replication
or remote tiering.

This is a **source and documentation audit**, not a live usability,
accessibility, security, performance, or protocol-conformance certification.
No MinIO or AIStor deployment was accessed. OpenS3's existing browser tests
were inspected, not executed; the host lacked `go` on PATH and the sandbox
could not access the Docker socket. “Yes” below means an implementation was
found in the UI and its handler, subject to the user's permissions. It does
not mean every edge case was exercised.

## What changed in MinIO

The reductions happened in stages. The archived changelog records that
**v1.7.4 removed the support-tool, site-replication, lifecycle, and tiering
interfaces**. MinIO's [lifecycle/tiering PR][removal-ilm] was merged on
2024-11-11; a [maintainer answer][removal-answer] identifies the December 2024
server release that included the lifecycle removal. The same changelog records
that **v2.0.0 removed account/policy, bucket-management, and configuration
administration**, directing users to `mc`. See `CHANGELOG.md` in the
[May 2025 archive][R-archive].

The [May 24, 2025 server release notes][removal-release] also describe the
embedded-console change and the removal of LDAP/OIDC UI login, while explicitly
saying that the STS APIs continue to work. Consequently, loss of a community
UI does **not** establish that the corresponding storage or administrative API
was removed or became paid-only.

The reduced source still contains useful object workflows: upload files and
folders, download, preview, share, tags, versions, restore, rewind, and deletion.
It also contains a basic create-bucket dialog. The dedicated administrative
pages and object retention/legal-hold editors are absent from that snapshot.
Thus “object browser only” should not be interpreted as “only upload and
download.” See [R1](#r1-reduced-community-snapshot).

Some features were gated even in the earlier full console. Its Health,
Performance, Profile, Inspect, and Call Home screens check
`registeredCluster()` and disable controls for an unregistered deployment.
Those are marked **H8 (gated)** below; their presence in open source is not
evidence that every community installation could use them. [H8]

## Reading the inventory

| OpenS3 status | Meaning |
|---|---|
| **Yes** | The described workflow has a UI and a corresponding implementation. Differences are noted. |
| **Partial** | A meaningful subset is exposed, often a read-only display, a simpler control, or incomplete workflow. |
| **Backend only** | Usable API, CLI, configuration, or lower-level primitives exist, but the described UI workflow does not. Notes identify any additional composition or endpoint work. |
| **No** | No working equivalent was found. A placeholder or related primitive that does not perform the feature is insufficient. |
| **N/A** | A MinIO-specific commercial/support workflow with no direct OpenS3 equivalent; not automatically an OpenS3 backlog item. |

`H1`–`H8` link to inspected historical source groups; `A0`–`A3` link to
current AIStor documentation. Historical evidence does not assert that a
feature survives in the reduced community UI. Links in the last column point
to OpenS3 source or documentation. A missing UI is checked against the complete
[route table][routes] and [browser routes][app], as well as the relevant view.

The rows are functional groups, not equally weighted units of engineering
work. They should not be used to calculate a product-parity percentage.

## Console access and navigation

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| A01 | Embedded web application: administer storage from a browser without installing a desktop client. | [H1], [A0] | Yes | Static assets are embedded in the server and served at `/console/`; there is no separate console listener. [routes] |
| A02 | Native user/root sign-in: authenticate with locally managed credentials. | [H1], [A0] | Yes | Username and console password, including configured root credentials. OpenS3 deliberately separates console passwords from S3 access keys. [login], [routes] |
| A03 | STS credential sign-in: enter temporary access key, secret, and session token. | [H1], [A0] | Backend only | S3 accepts temporary credentials, but the login form and handler accept username/password only. Internally issuing an STS session after login is a different workflow. [STS], [login] |
| A04 | OpenID Connect: configure identity providers and sign in through them. | [H1], [A2] | No | No OIDC configuration screen, callback, or web-identity STS flow. [routes], [STS] |
| A05 | AD/LDAP: configure directory authentication and related access mappings. | [H1], [A2] | No | Local IAM only; no LDAP provider configuration or LDAP STS flow. [routes], [STS] |
| A06 | Change own password and administrator password reset. | [H1], [H3] | Yes | Non-root users have “Change password”; administrators set or remove another user's console password. Root credentials remain server configuration. [app], [identity], [admin UI API] |
| A07 | Session handling and logout: end browser access and handle expired sessions. | [H1] | Yes | Twelve-hour sessions, expiry handling, and logout that revokes the session credential. [routes], [app] |
| A08 | Permission-aware navigation and actions: tailor controls to the current identity. | [H1] | Partial | User/admin navigation exists and handlers authorize actions. Administration visibility uses one coarse `admin` flag requiring both `opens3:ServerInfo` and `iam:ListUsers`; it is not a per-action menu capability model. [app], [routes] |
| A09 | Light/dark appearance. | [H1] | Yes | System, light, and dark themes are configurable per browser. [settings], [app] |
| A10 | Keyboard navigation and searchable command palette. | [H1] | Partial | `/`, `r`, `u`, `Esc`, and `,` shortcuts exist; no searchable action palette. [app], [UI helpers] |
| A11 | Contextual help and documentation links from console screens. | [H1] | Partial | OpenS3 has inline field explanations, but no equivalent help menu or linked help library in the console. Repository manuals exist separately. [buckets], [identity], [app] |

## Buckets and bucket configuration

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| B01 | List and filter accessible buckets. | [H2], [A1] | Yes | Bucket list, name filter, creation date, region, owner, and feature badges. [buckets], [bucket API] |
| B02 | Create buckets, validating names and choosing initial options. | [H2], [A1] | Yes | Creation dialog offers versioning, Object Lock, and tags. Default retention is a separate gap, B07. [buckets], [bucket API] |
| B03 | Delete an empty bucket. | [H2] | Yes | Delete controls in the list and settings; versions and delete markers must also be removed. [buckets], [bucket API] |
| B04 | Enable or suspend versioning, preserving object history. | [H2], [A1] | Yes | Versioning selector; suspension is prevented while Object Lock is enabled. [buckets], [bucket API] |
| B05 | Exclude selected prefixes or folder markers from versioning. | [H2] | No | OpenS3 implements bucket-wide versioning, with no MinIO-style exclusion configuration or behavior. [S3 bucket], [object service] |
| B06 | Enable Object Lock when creating a bucket. | [H2], [A1] | Yes | Creation checkbox enables the lock-capable bucket and versioning. This alone does not set a retention period. [buckets], [bucket API] |
| B07 | Configure default retention: governance/compliance mode and a duration for new objects. | [H2] | Partial | Bucket overview displays an existing retention configuration but cannot edit it. S3 `PutObjectLockConfiguration` is implemented. [buckets], [S3 bucket] |
| B08 | Bucket configuration summary: inspect ownership, protection, and enabled settings. | [H2], [A1] | Yes | Settings overview exposes these properties, including presence of lifecycle, CORS, and notifications. Configuration editing is assessed separately. [buckets], [bucket API] |
| B09 | Per-bucket usage: object counts and stored bytes. | [H2] | Backend only | No bucket-usage card. The admin API computes aggregate usage using `bucketUsage`; exposing per-bucket totals needs an endpoint or aggregation. That helper counts stored versions, excluding delete markers. [admin API], [buckets] |
| B10 | View/edit/remove a bucket access policy as JSON. | [H2] | Yes | Validated policy editor, remove action, public-read example, and public-policy badge. [buckets], [bucket API] |
| B11 | Anonymous-access rules by prefix, using guided read/write controls. | [H2] | Partial | Equivalent prefix rules can be written in the JSON bucket policy; there is no guided rule table/editor. The public-read example applies to the whole bucket. [buckets] |
| B12 | Access inspection: show which identities and policies grant access to a bucket. | [H2], [A1] | Backend only | Users/groups/policies are inspectable, but no bucket-centric access report or effective-permission calculation is exposed. A report needs policy evaluation and aggregation. [identity], [IAM], [buckets] |
| B13 | Configure default encryption and a default KMS key. | [H2], [A1] | Yes | None, SSE-S3, or SSE-KMS with a named key. Existing objects are not rewritten by this setting. [buckets], [bucket API] |
| B14 | Add, change, and remove bucket tags. | [H2], [A1] | Yes | Editable key/value tag list during creation and in settings. [buckets], [bucket API] |
| B15 | Hard bucket quota: limit stored data and reject writes beyond the limit. | [H2], [A1] | No | `meta.Quota` and an overview display exist, but no setter or enforcement was found in the storage write paths. A displayed field is not a working quota. [metadata], [bucket API], [object service] |
| B16 | Lifecycle expiration rules: automatically remove eligible current/noncurrent versions and expired markers. | [H2], [A1] | Partial | UI only says lifecycle configuration exists. S3 configuration and the expiration worker work; a rule viewer/editor is missing. [buckets], [S3 bucket], [lifecycle] |
| B17 | Bucket notification rules: select events, prefix/suffix filters, and a destination. | [H2], [A1] | Partial | UI only reports notification configuration presence. S3 configuration and webhook delivery exist. Target administration is N01–N02. [buckets], [S3 bucket], [notifications] |
| B18 | Bucket replication rules: configure destination, filters, and replication options. | [H2], [A1] | No | Replication XML is stored and minimally validated; no transfer worker or console editor exists. This is not functioning replication. [S3 bucket], [API coverage] |

## Objects, folders, and versions

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| O01 | Navigate prefixes as folders using breadcrumbs. | [H4] | Yes | Delimiter-based folders, breadcrumb navigation, and load-more pagination. [objects], [object API] |
| O02 | Find objects and sort the listing by name, date, or size. | [H4] | Partial | Filter searches only already-loaded entries in the current folder; no sortable columns. Loading more can reveal additional matches. [objects] |
| O03 | Upload one or several files, including drag-and-drop. | [H4], [A1] | Yes | File picker and drop area upload multiple files with aggregate progress. [objects], [client API], [object API] |
| O04 | Upload a directory while preserving its hierarchy. | [H4] | Backend only | S3 keys can preserve paths, but the picker lacks folder selection and upload uses `File.name`, not a relative directory path. [objects], [client API] |
| O05 | Transfer manager: inspect individual transfers and cancel work. | [H4] | Partial | One progress toast per upload request; no per-file queue, individual cancellation control, or transfer history panel. [objects], [client API] |
| O06 | Download an object or a selected version. | [H4], [A1] | Yes | Download links carry the version ID when applicable. The console cannot supply an SSE-C customer key. [objects], [object API] |
| O07 | Preview supported object content. | [H4], [A1] | Yes | “Open” displays allowed types in a new tab: common images, text/JSON/CSV, PDF, audio, and video. Active HTML/SVG is downloaded instead. This differs from MinIO's embedded preview modal. [objects], [object API] |
| O08 | Download an entire folder or bucket as ZIP. | [H4] | Yes | “Download folder” / “Download all” stream current objects. Unreadable and SSE-C objects are skipped and recorded in the archive. [objects], [folder API] |
| O09 | Download an arbitrary selection of multiple objects as ZIP. | [H4], [A1] | Backend only | Individual downloads and prefix ZIPs exist, but the ZIP endpoint accepts a prefix, not an arbitrary selected-object list. [objects], [folder API] |
| O10 | Create a folder/prefix. | [H4], [A1] | Yes | Creates a zero-byte folder-marker object and navigates into it. [objects], [object API] |
| O11 | Delete individual objects or a selected batch. | [H4], [A1] | Yes | Single and multi-selection deletion with confirmation and per-object failures. [objects], [object API] |
| O12 | Recursively delete a prefix, optionally including its versions. | [H4] | Yes | Prefix deletion pages through matches; the dedicated folder action shows count/size before confirmation. Version mode permanently removes versions and markers, subject to retention. [objects], [folder API] |
| O13 | List versions and show deleted objects/delete markers. | [H4], [A1] | Yes | “Show versions,” latest-version badges, marker rows, and pagination. [objects], [object API] |
| O14 | Permanently delete selected versions or delete markers. | [H4] | Yes | Select rows in version mode and use “Delete selected.” Deleting a current delete marker can make a surviving older version visible again. [objects], [object API] |
| O15 | Restore a chosen historical version as the current object. | [H4] | Backend only | Version-aware `CopyObject` provides the primitive, but no restore-version control or console handler exists. This is not the archive-retrieval `RestoreObject` API. [copy], [S3 object], [routes] |
| O16 | Rewind the browser to an earlier date/time. | [H4], [A1] | Backend only | Version listing is available, but there is no date selector or calculation of the visible version at that time. MinIO's rewind is a historical view, not an automatic bulk rollback. [objects], [object API] |
| O17 | Remove all noncurrent versions of an object in one operation. | [H4] | Partial | Versions can be selected manually; no “delete noncurrent versions” shortcut. Prefix deletion in version mode removes current versions too, so it is not equivalent. [objects], [folder API] |
| O18 | Explicitly bypass governance retention during an authorized deletion. | [H4] | Backend only | S3 and console deletion handlers support governance bypass and check permission, but browser confirmations expose no bypass checkbox. Compliance retention remains a separate restriction. [object API], [folder API] |
| O19 | Inspect ordinary object properties, headers, and user metadata. | [H4], [A1] | Yes | Detail panel shows key, size, times, ETag, version, content headers, storage class, checksum, encryption, and user metadata. This is distinct from MinIO's diagnostic “Inspect” tool, S05. [objects], [object API] |
| O20 | View and edit object tags. | [H4], [A1] | Partial | Tags are read-only in the detail panel; S3 get/put/delete tagging handlers exist. [objects], [S3 object] |
| O21 | View and change an object's retention mode/date. | [H4], [A1] | Partial | Retention is displayed; mutation is available through S3 but not a console control. [objects], [S3 object] |
| O22 | View, apply, and release an object's legal hold. | [H4], [A1] | Partial | Legal hold is displayed; S3 supports changing it, but no UI switch exists. [objects], [S3 object] |
| O23 | Generate a shareable, expiring download URL, including a version when appropriate. | [H4], [A1] | Backend only | SigV4 presigning/verification exists. UI links point to authenticated console downloads, so copying one is not an independent share link. Add a scoped signing workflow and expiry controls. [signatures], [client API] |

## Identities, policies, and application credentials

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| I01 | List, filter, create, and delete local users. | [H3], [A2] | Yes | Identity → Users; optional console password and generated initial access key. [identity], [admin UI API] |
| I02 | Enable/disable users and attach/detach their policies. | [H3], [A2] | Yes | User edit dialog handles status and policies; password management is A06. [identity], [admin UI API] |
| I03 | Manage groups, membership, enabled state, and attached policies. | [H3], [A2] | Yes | Group create/edit/delete with multiple-member and policy selectors. User group membership is changed from the group editor. [identity], [admin UI API] |
| I04 | View built-in policies and create/edit/delete custom JSON policies. | [H3], [A2] | Yes | Built-ins are read-only; custom policies have a validated JSON editor. [identity], [admin UI API] |
| I05 | Policy-centric inspection of attached users and groups. | [H3] | Backend only | Attachments appear in user/group lists, but the policy view does not show reverse membership. Existing IAM data can support such a view. [identity], [IAM] |
| I06 | Self-service access keys: users manage their own application credentials. | [H3], [A2] | Yes | Non-admin users get “My access keys,” including their own service accounts. [identity], [admin UI API] |
| I07 | Administrators manage another user's access keys/service accounts. | [H3], [A2] | Yes | Admin key list, owner selector, and user-to-keys navigation. [identity], [admin UI API] |
| I08 | Restrict a service account with a policy narrower than its parent's. | [H3], [A2] | Yes | Service-account creation and editing expose a JSON session policy. Parent permissions still bound access. [identity], [IAM] |
| I09 | Credential labels, descriptions, and expiration management. | [H3] | Partial | Description and creation-time expiry exist. No separate name/comments fields, and no expiry editing for an existing key. [identity], [admin UI API] |
| I10 | Enable, disable, delete, and replace application credentials. | [H3] | Yes | Key status/delete controls, plus generated-secret rotation in Edit. MinIO's documented replacement-key workflow is also possible by creating a new key then deleting the old one. [identity], [admin UI API] |
| I11 | Choose an access-key ID/secret instead of accepting generated values. | [H3], [A2] | Backend only | The console deliberately rejects chosen values. The separate admin API can provision supplied credentials; this is an intentional UI difference, not necessarily a desirable change. [admin UI API], [admin API] |
| I12 | Export newly issued credentials as a downloadable client-import file. | [H3] | Partial | Secrets are shown once and can be copied. No `credentials.json` / client-alias export button exists. [identity] |

## Notifications, tiering, replication, and batch jobs

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| N01 | Create, inspect, edit, and remove webhook event destinations. | [H5], [A3] | Backend only | Webhook targets are configured through environment variables; no target-management UI or API. Bucket rule editing is B17. [notifications], [routes] |
| N02 | Configure event destinations for message brokers and databases. | [H5] | No | MinIO offers AMQP, Kafka, MQTT, NATS, NSQ, Redis, Elasticsearch, MySQL, and PostgreSQL. OpenS3 ships only a webhook implementation; a plugin interface does not implement those transports. [notifications] |
| N03 | Watch bucket events live, with bucket/event selection and start/stop controls. | [H6] | Backend only | Object events and webhook delivery exist, but there is no browser event stream, watch endpoint, or viewer. An interactive feed still needs implementation. [notifications], [routes] |
| N04 | Define remote storage tiers for MinIO/S3, Amazon S3, Azure, or Google Cloud Storage. | [H5], [A1] | No | No remote-tier registry or storage connector implementation. Local storage-class metadata is not a remote tier. [lifecycle], [API coverage] |
| N05 | Manage tier credentials and inspect tier status/usage. | [H5], [A1] | No | Requires N04 and tier telemetry; no corresponding UI/server feature. [routes], [API coverage] |
| N06 | Lifecycle transition rules that actually move object data to remote tiers. | [H2], [H5], [A1] | No | The lifecycle worker changes `StorageClass` metadata but does not move bytes. The future UI must not imply archival or cost savings from that label alone. [lifecycle] |
| N07 | Site replication: establish and manage peers that synchronize buckets and identity configuration. | [H5], [A3] | No | No site-replication protocol, peer registry, or UI. Stored bucket replication XML does not implement this. [routes], [API coverage], [roadmap] |
| N08 | Inspect replication status and look up synchronization of buckets/users/groups/policies. | [H5] | No | No worker or replication progress/entity-status data. These are separate requirements from merely accepting configuration. [API coverage], [routes] |
| N09 | Create and monitor batch jobs through the console. | [A1] | No | No general job registry, job editor, progress view, or batch execution API. [routes], [admin API] |
| N10 | Batch replication of selected existing objects. | [A1] | No | No batch replication engine. Normal `CopyObject` within this server is not cross-deployment replication. [copy], [API coverage] |
| N11 | Batch expiration using a job definition. | [A1] | No | Scheduled lifecycle expiration exists, but not an independently submitted and tracked batch-expiration job. [lifecycle], [routes] |
| N12 | Batch rotation of object encryption keys. | [A1] | No | OpenS3's offline master-key rotation/rewrapping is a different operation; there is no MinIO-style batch job selecting and re-encrypting objects. [master rotation], [routes] |

## Monitoring and operational visibility

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| M01 | Show version, uptime, and basic server details. | [H6], [A3] | Yes | Status page exposes version/region/uptime; authorized users additionally see host, runtime, memory, and endpoint details. [status], [admin UI API] |
| M02 | Capacity overview: total, used, and available storage. | [H6], [A3] | Yes | Disk card with totals and usage bar. These are filesystem-capacity figures, not a sum of logical object sizes. [status], [admin UI API] |
| M03 | Deployment summary: bucket/object totals, identity counts, and event destinations. | [H6], [A3] | Partial | Bucket/user/key counts exist; object count/logical bytes and configured destinations are absent from the UI. Aggregate object usage is available from the separate admin API. [status], [admin API] |
| M04 | Server/drive/pool health and topology, including erasure-storage information. | [H6], [A3] | No | One host and filesystem-usage card do not provide distributed node/drive health, pools, or parity information. Distributed storage is later roadmap work. [status], [roadmap] |
| M05 | Usage, traffic, and resource dashboards backed by Prometheus. | [H6] | Backend only | S3 request/error counts, latency, byte counters, Go/process metrics, and notification metrics exist, but no charting or Prometheus query integration. [metrics], [notifications], [status] |
| M06 | Historical chart controls and export of metric widget data. | [H6] | No | No time-series query UI, history store, or chart export. A metrics scrape endpoint alone supplies neither history nor this workflow. [metrics], [status] |
| M07 | Live server/error-log viewer. | [H6] | Backend only | Server logging exists, but logs are not exposed through a console stream/viewer. [server], [routes] |
| M08 | Search audit history by operation, identity, bucket/object, status, and request ID. | [H6] | No | Ordinary logs and bucket event notifications are not a complete searchable audit trail. No audit collector/search backend or console interface was found. MinIO's log-search setup also needs an external log store. [server], [routes], [A-settings] |
| M09 | Live API tracing with request/response inspection and filters. | [H6], [A3] | No | Aggregate metrics exist, but there is no request-trace stream or viewer. [metrics], [routes] |

## Server configuration and key management

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| C01 | View/change site or region configuration. | [H7], [A3] | Backend only | Region and other startup settings are configurable outside the UI; Status is read-only. No server-configuration editor. [server], [status], [configuration] |
| C02 | API tuning, including global allowed CORS origins and request/worker settings. | [H7] | Backend only | OpenS3 has some startup limits and bucket CORS support, but no matching live API-settings screen or complete MinIO settings model. Per-bucket CORS is a distinct configuration. [server], [S3 bucket], [configuration] |
| C03 | Configure transparent compression by file type/extension. | [H7], [A3] | No | No transparent storage-compression configuration or implementation found. Storing a client-supplied compressed object is different. [object service], [server] |
| C04 | Configure healing and storage-scanner behavior. | [H7] | No | Lifecycle scheduling and offline filesystem checks exist, but no distributed healing/scanner configuration model or console. [lifecycle], [fsck], [roadmap] |
| C05 | Configure an external etcd integration. | [H7] | No | OpenS3 uses its local metadata store, with no etcd integration or console panel. Historical/version-dependent MinIO feature. [server], [format] |
| C06 | Configure server logger webhooks and audit webhook/Kafka sinks. | [H7], [A3] | No | Bucket-event webhooks are implemented, but they are not configurable server-log/audit sinks. [notifications], [server] |
| C07 | Export/import server configuration through the browser. | [H7], [A3] | No | No configuration import/export endpoint or view. `opens3 export` exports stored data, not MinIO-style server configuration. [routes], [export] |
| C08 | Restart the server from the console after configuration changes. | [H1], [H7] | No | No restart control or administrative restart endpoint. [routes], [admin API] |
| C09 | List and create named server-side encryption keys. | [H7], [A3] | Yes | Encryption keys page handles names and creation. OpenS3 uses a built-in local KMS; this is workflow parity, not external KMS integration. [keys UI], [admin UI API], [local KMS] |
| C10 | Delete an encryption key. | [A3] | Yes | Named-key deletion with a warning; the default key is protected in the UI. This can make encrypted objects unreadable. [keys UI], [admin UI API] |
| C11 | KMS health/status, connected endpoints, supported APIs, and request metrics. | [H7] | No | Key inventory/status badges exist, but no external KMS service-health, API, or metrics dashboard. [keys UI], [local KMS] |

## Diagnostics and MinIO-specific support

| ID | MinIO feature and purpose | MinIO evidence | OpenS3 | OpenS3 implementation or gap |
|---|---|---|---|---|
| S01 | View license/subscription details and register a deployment with SUBNET. | [H8], [A3] | N/A | No commercial license activation or MinIO support registration. This is not required to match storage-management capability. [governance] |
| S02 | Generate/download a deployment diagnostic report for support. | [H8] (gated), [A3] | No | Health endpoints and offline `fsck` are narrower than a downloadable host/TLS/drive/cluster diagnostic report. [status], [fsck], [routes] |
| S03 | Run a configurable S3 GET/PUT performance test and inspect results. | [H8] (gated), [A3] | No | No built-in benchmark runner or results screen. [routes] |
| S04 | Start/stop CPU, memory, contention, or goroutine profiling and download results. | [H8] (gated), [A3] | No | Displaying current heap/goroutine counts does not provide process profiling. No profiling endpoint/UI was found. [status], [routes] |
| S05 | Diagnostic Inspect: collect underlying object/path data into a support bundle, optionally encrypted. | [H8] (gated), [A3] | No | Ordinary object metadata is visible, and offline recovery tools exist, but there is no equivalent diagnostic bundle workflow. [objects], [fsck], [routes] |
| S06 | Call Home: configure automated diagnostic submission to MinIO support. | [H8] (gated), [A3] | N/A | No MinIO support relationship or automatic diagnostic-upload workflow. Any OpenS3 counterpart would be a separate product decision. [governance], [routes] |

## Additional commercial Global Console scope

These are publicly described feature families, not an exhaustive inventory of
proprietary dialogs. They are kept separate because the embedded console source
cannot establish how every Global Console screen behaves. MinIO describes this
as a console for multiple deployments and additional services. [G1]

| ID | Published Global Console capability | Evidence | OpenS3 | Gap |
|---|---|---|---|---|
| G01 | Manage multiple deployments from one UI. | [G1] | No | OpenS3's UI talks to its own server only. [client API] |
| G02 | Deploy, configure, upgrade, and monitor storage across environments. | [G-overview] | No | No fleet deployment/update orchestration. [app], [roadmap] |
| G03 | Kubernetes-aware management using Operator capabilities. | [G1] | No | No tenant/Operator management UI. [app], [roadmap] |
| G04 | Delegate access per feature and per deployment. | [G1] | No | IAM applies to this server's resources; there is no fleet-level authorization model. [IAM], [app] |
| G05 | Catalog: search indexed object namespace and metadata. | [G1], [G-overview] | No | The object-name filter is not a metadata index or cross-deployment search service. [objects] |
| G06 | Administer a data firewall. | [G1], [G2] | No | Bucket/IAM policies exist, but no separate data-firewall service or console. [IAM], [app] |
| G07 | Administer caching services. | [G1], [G2] | No | No corresponding cache-management service/UI. [server], [app] |
| G08 | Administer load balancing. | [G2] | No | Deployment can use an external proxy, but the console does not provision/manage load balancers. [configuration], [app] |
| G09 | Use `promptObject` to interact with unstructured objects through AI. | [G2] | No | No object-prompting API or interface. [S3 routes], [app] |

## Highest-value next work

These are recommendations from the implementation comparison, not an accepted
roadmap or changes made by this audit.

| Priority | Work | Why and what completion should demonstrate |
|---|---|---|
| 1 | Lifecycle viewer/editor (B16) and bucket notification-rule editor (B17). | Both already have functioning S3 configuration and execution paths. Show the actual rules, allow edits/deletion, and verify the resulting behavior. Expose only working expiration behavior; distinguish metadata-only transitions. |
| 1 | Object tag, retention, legal-hold, and bucket default-retention editors (O20–O22, B07). | These are existing backend capabilities hidden behind read-only UI. Preserve version IDs and check each specific permission; test denied operations and locked-object restrictions. |
| 1 | Expiring share links and restore-version actions (O23, O15). | Common browser workflows with existing signing and version-aware copy primitives. Verify a shared URL without a console cookie, expiry, and restoration to a new current version without losing history. |
| 2 | Folder upload, sorting, arbitrary-selection ZIPs, and transfer cancellation (O02, O04, O05, O09). | Mostly console work. Validate nested paths, pagination/filter behavior, partial failures, and cancellation. Avoid describing HTTP multipart/form-data as resumable S3 multipart upload. |
| 2 | Per-bucket/aggregate object usage and initial traffic/error/latency charts (B09, M03, M05). | Some data already exists. Define whether counts mean current objects or all versions, and whether bytes mean logical object data or filesystem usage. Historical charts need a time-series source. |
| 2 | Finer permission-driven navigation and access explanations (A08, B12, I05). | Let delegated administrators discover the actions they are actually authorized to perform, and explain why a user or policy grants access. |
| 3 | Enforced quotas, OIDC/LDAP, replication, and remote tiering (B15, A04–A05, B18, N04–N08). | These require backend design and implementation before an operational UI can be truthful. Replication and tiering are substantially larger than adding editors for stored XML. |
| Separate decision | Support bundles, profiling, fleet operations, and AIStor-specific services. | Assess their fit with OpenS3's single-node phase and project goals. MinIO license registration and Call Home are not necessary parity targets. |

## Boundary cases and documentation issues

- **OpenS3 login documentation conflicts with the code.** The early “Logging
  in” section of [CONSOLE.md](CONSOLE.md) says access-key pairs and service
  accounts can sign in; its later “Signing in and credentials” section says
  they cannot. The [current login handler][routes], [login view][login], and
  [browser tests][smoke] support username/password-only sign-in. This audit
  follows the implementation. The document also uses legacy `admin:*`
  permission names where the current code uses `iam:*`, `opens3:*`, and
  `kms:*` actions.
- **Quota is a placeholder.** A repository-wide search found a metadata
  structure, error code, and display, but no setting/enforcement implementation.
  Replication XML storage and transition-label updates have similar limitations,
  explicitly recorded above.
- **OpenS3 has useful features beyond this checklist.** Browser density,
  configurable date/time-zone and size formats, page-size selection, folder-marker
  visibility, separated console passwords, and built-in local KMS are already
  present. These should not be lost while pursuing parity. [settings], [app],
  [local KMS]
- **Do not infer additional MinIO GUI features from S3 support.** A generic
  object copy/move/rename editor, arbitrary metadata editor, S3 Select query
  console, and resumable multipart-upload management were not established by
  the inspected routes/screens. They are not counted as confirmed MinIO GUI
  features here. OpenS3 API support for some of those operations is a separate
  question. [H4], [API coverage]
- **Per-bucket CORS deserves its own OpenS3 editor**, since the S3 implementation
  exists and the UI only reports its presence. The checked historical MinIO
  configuration screen exposed global API CORS origins; that is not proof of a
  per-bucket CORS editor, so this is an additional OpenS3 opportunity rather than
  a confirmed parity row. [H7], [buckets], [S3 bucket]
- **The commercial inventory has a verification boundary.** A live, licensed
  AIStor/Global Console walkthrough would be needed to enumerate every current
  dialog and validate edition-specific gates. AIHub, AIStor Tables, RDMA,
  inventory reporting, and other server/product capabilities are not silently
  counted as console workflows without direct UI evidence.

## Source register and reproducibility

### Archived MinIO source provenance

The archives contain upstream source; no MinIO implementation code or assets
were copied into OpenS3. Paths below are relative to the module root inside
each ZIP. Most historical UI paths begin with
`web-app/src/screens/Console/`.

| Archive | SHA-256 of downloaded ZIP |
|---|---|
| [Historical v1.7.3][H-archive] | `4e2388148ce922e77dcc25d78793de97264761a4cf8017c56a39beac8a75e7dc` |
| [Reduced May 2025 snapshot][R-archive] | `e38ea23187d9fbae767efe0f0ff79638af261ac7cf2c9facce43b76d504baba5` |

### H1: Console shell and authentication

In the historical archive: `web-app/src/screens/LoginPage/StrategyForm.tsx`,
`Console/Console.tsx`, `Console/valid-routes.tsx`, `Console/ConsoleKBar.tsx`,
`Console/kbar-actions.tsx`, `Console/HelpMenu.tsx`,
`Console/Common/DarkModeActivator/`, and `Console/Account/ChangePasswordModal.tsx`.
Here and below, `Console/` abbreviates `web-app/src/screens/Console/`.
These establish the actual route/menu inventory, login strategies, navigation,
appearance, and contextual help. OIDC/LDAP management is also documented by [A2].

### H2: Bucket administration

Historical `Console/Buckets/ListBuckets/AddBucket/` and
`Console/Buckets/BucketDetails/`. Key files:
`BucketDetails.tsx`, `BucketSummaryPanel.tsx`, `AccessDetailsPanel.tsx`,
`AccessRulePanel.tsx`, `SetAccessPolicy.tsx`, `EnableVersioningModal.tsx`,
`EnableBucketEncryption.tsx`, `EnableQuota.tsx`, `SetRetentionConfig.tsx`,
`BucketLifecyclePanel.tsx`, `AddLifecycleModal.tsx`,
`EditLifecycleConfiguration.tsx`, `BucketEventsPanel.tsx`, `AddEvent.tsx`,
`BucketReplicationPanel.tsx`, `AddBucketReplication.tsx`, and
`EditBucketReplication.tsx`. Bucket-tag controls are in the same directory.

### H3: Identity and access keys

Historical `Console/Users/`, `Console/Groups/`, `Console/Policies/`,
`Console/Account/`, and `Console/Common/CredentialsPrompt/`.
Relevant concrete controls include `Users/UserDetails.tsx`,
`Users/UserServiceAccountsPanel.tsx`, `Policies/PolicyDetails.tsx`,
`Account/AddServiceAccountScreen.tsx`, `Account/EditServiceAccount.tsx`, and
`Common/CredentialsPrompt/CredentialsPrompt.tsx`. These substantiate account
administration, narrower key policies, expiry fields, and credential-file export.

### H4: Object browsing and transfers

Historical `Console/ObjectBrowser/` and
`Console/Buckets/ListBuckets/Objects/`, particularly:

- `ListObjects/ListObjects.tsx`, `ListObjects/ListObjectsTable.tsx`,
  `ListObjects/CreatePathModal.tsx`, and `ListObjects/ObjectDetailPanel.tsx`;
- `ListObjects/DeleteObject.tsx`, `DeleteMultipleObjects.tsx`,
  `DeleteNonCurrent.tsx`, and `RewindEnable.tsx`;
- `ObjectDetails/ShareFile.tsx`, `RestoreFileVersion.tsx`,
  `DeleteSelectedVersions.tsx`, `VersionsNavigator.tsx`, `ObjectMetaData.tsx`,
  `TagsModal.tsx`, `SetRetention.tsx`, and `SetLegalHoldModal.tsx`;
- `Preview/`, plus `Console/Buckets/ListBuckets/UploadFilesButton.tsx` and
  `Console/Common/ObjectManager/ObjectHandled.tsx`.

The table controls implement sorting; directory upload sets `webkitdirectory`;
the transfer manager aborts individual in-progress requests. Rewind sets a
historical-view timestamp, while `RestoreFileVersion` invokes a distinct
restore operation. These details prevent several misleading equivalences.

### H5: Destinations, tiers, and site replication

Historical `Console/EventDestinations/`,
`Console/Configurations/TiersConfiguration/`, and
`Console/Configurations/SiteReplication/`. Files include
`EventTypeSelector.tsx`, `ListEventDestinations.tsx`, destination-specific
forms, `TierTypeSelector.tsx`, `ListTiersConfiguration.tsx`,
`UpdateTierCredentialsModal.tsx`, `DeleteTierConfirmModal.tsx`,
`SiteReplication.tsx`, `SiteReplicationStatus.tsx`, `EditSiteEndPoint.tsx`,
and `EntityReplicationLookup.tsx`.

### H6: Metrics, logs, tracing, and watch

Historical `Console/Dashboard/BasicDashboard/BasicDashboard.tsx`,
`Console/Dashboard/Prometheus/PrDashboard.tsx`,
`Console/Dashboard/DownloadWidgetDataButton.tsx`,
`Console/Logs/ErrorLogs/ErrorLogs.tsx`,
`Console/Logs/LogSearch/LogsSearchMain.tsx`, `Console/Trace/Trace.tsx`, and
`Console/Watch/Watch.tsx`. Advanced charts depend on Prometheus integration;
log search depends on separately configured collection/storage.

### H7: Server configuration and KMS

Historical `Console/Configurations/utils.tsx`,
`Console/Configurations/ConfigurationPanels/ConfigurationOptions.tsx`,
`ConfigurationForm.tsx`, `ExportConfigButton.tsx`, `ImportConfigButton.tsx`,
and `Console/KMS/KMSRoutes.tsx`, `Status.tsx`, `ListKeys.tsx`, `AddKey.tsx`.
`Console/Console.tsx` contains the restart call and control.
The configuration registry explicitly lists Region, Compression, API, Heal,
Scanner, Etcd, Logger Webhook, Audit Webhook, and Audit Kafka. Do not assume
all of those historical panels exist in every current deployment.

### H8: Support and registration gates

Historical `Console/License/`, `Console/Support/Register.tsx`,
`Console/HealthInfo/HealthInfo.tsx`, `Console/Speedtest/Speedtest.tsx`,
`Console/Support/Profile.tsx`, `Console/Tools/Inspect.tsx`, and
`Console/Support/CallHome.tsx`. The latter five contain explicit
`registeredCluster()` checks and disabled controls. `Console/Tools/Tools.tsx`
and `Console/valid-routes.tsx` establish their routes/menu entries.

### R1: Reduced community snapshot

In the reduced archive, inspect `CHANGELOG.md`, `Console/Console.tsx`,
`Console/Menu/MenuWrapper.tsx`, `Console/Buckets/ListBuckets/AddBucket/`, and
`Console/Buckets/ListBuckets/Objects/ListObjects/ObjectDetailPanel.tsx`.
The detail-panel action list retains Download, Share, Preview, Tags, and
versions; it no longer contains the full console's legal-hold, retention,
or diagnostic-inspect editors. The object browser still contains rewind,
folder upload, and restoration components. Routes/features remain subject
to the server and identity permissions; retained files alone are not a claim
that every deployment displays every control.

### Maintaining this audit

For an update, record the new OpenS3 commit and MinIO release/edition first.
Then follow each changed browser control through its console route to the
underlying operation. Recheck negative claims using the complete route lists,
and distinguish stored configuration from executed behavior. Run relevant
existing console tests and add a live walkthrough if a runnable environment
is available. Update row statuses and limitations together; a new settings
field by itself should not change a row to “Yes.”

[H-archive]: https://proxy.golang.org/github.com/minio/console/@v/v1.7.3.zip
[H-origin]: https://proxy.golang.org/github.com/minio/console/@v/v1.7.3.info
[R-archive]: https://proxy.golang.org/github.com/minio/console/@v/v1.7.7-0.20250516212319-220a55500cc3.zip
[R-origin]: https://proxy.golang.org/github.com/minio/console/@v/v1.7.7-0.20250516212319-220a55500cc3.info
[H1]: #h1-console-shell-and-authentication
[H2]: #h2-bucket-administration
[H3]: #h3-identity-and-access-keys
[H4]: #h4-object-browsing-and-transfers
[H5]: #h5-destinations-tiers-and-site-replication
[H6]: #h6-metrics-logs-tracing-and-watch
[H7]: #h7-server-configuration-and-kms
[H8]: #h8-support-and-registration-gates
[A0]: https://docs.min.io/aistor/administration/console/
[A1]: https://docs.min.io/aistor/administration/console/managing-objects/
[A2]: https://docs.min.io/aistor/administration/console/security-and-access/
[A3]: https://docs.min.io/aistor/administration/console/managing-deployment/
[A-settings]: https://docs.min.io/aistor/reference/aistor-server/settings/console/
[G1]: https://min.io/product/aistor/object-storage-global-console
[G2]: https://www.min.io/blog/introducing-aistor
[G-overview]: https://min.io/product/aistor-overview
[removal-ilm]: https://github.com/minio/object-browser/pull/3470
[removal-answer]: https://github.com/minio/minio/discussions/21271
[removal-release]: https://github.com/minio/minio/releases/tag/RELEASE.2025-05-24T17-08-30Z
[app]: ../internal/console/static/app.js
[routes]: ../internal/console/console.go
[login]: ../internal/console/static/views/login.js
[settings]: ../internal/console/static/settings.js
[UI helpers]: ../internal/console/static/ui.js
[buckets]: ../internal/console/static/views/buckets.js
[bucket API]: ../internal/console/buckets.go
[objects]: ../internal/console/static/views/objects.js
[object API]: ../internal/console/objects.go
[folder API]: ../internal/console/folders.go
[client API]: ../internal/console/static/api.js
[identity]: ../internal/console/static/views/identity.js
[admin UI API]: ../internal/console/admin.go
[admin API]: ../internal/admin/handler.go
[IAM]: ../internal/iam/store.go
[keys UI]: ../internal/console/static/views/kms.js
[local KMS]: ../internal/kms/kms.go
[status]: ../internal/console/static/views/status.js
[S3 bucket]: ../internal/s3api/bucket.go
[S3 object]: ../internal/s3api/object.go
[S3 routes]: ../internal/s3api/router.go
[STS]: ../internal/s3api/sts.go
[object service]: ../internal/object/service.go
[copy]: ../internal/object/copy.go
[metadata]: ../internal/meta/types.go
[lifecycle]: ../internal/lifecycle/worker.go
[notifications]: NOTIFICATIONS.md
[metrics]: ../internal/server/metrics.go
[signatures]: ../internal/auth/sigv4/sigv4.go
[server]: ../internal/server/server.go
[master rotation]: ../internal/masterkey/rewrap.go
[fsck]: ../internal/fsck/fsck.go
[export]: ../cmd/opens3/export.go
[smoke]: ../tests/console/smoke.spec.js
[API coverage]: API-COVERAGE.md
[roadmap]: PLAN.md
[format]: FORMAT.md
[configuration]: manual/src/02-configuration.md
[governance]: ../GOVERNANCE.md
