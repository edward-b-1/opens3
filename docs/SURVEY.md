# Survey: Amazon S3, MinIO, the alternatives, and where OpenS3 fits

Compiled 12 September 2026. Every factual claim was checked against a primary source on that date; sources are linked inline. This document informs the plan in `PLAN.md` and will be revised as decisions are taken.

Contents:

1. Implementation language (comparison; decision pending)
2. What is bbolt?
3. Amazon S3 feature survey
4. MinIO: commercialisation, feature reduction and current state
5. Open-source and other S3-compatible alternatives
6. Positioning: what would make OpenS3 the best option

---

## Implementation language (comparison; decision pending)

The candidates were Go, Rust, C/C++ and Python. The workload is an HTTP
server that streams large byte ranges to and from disk, hashes everything
(MD5, SHA-256, CRC32C, CRC64-NVMe), encrypts with AES-GCM, and will
eventually do Reed-Solomon erasure coding across a cluster. It must run for
years without leaking or crashing, and must be trivially deployable as one
static binary on x86 and ARM.

| Criterion | Go | Rust | C / C++ | Python |
|---|---|---|---|---|
| Throughput for streaming I/O | Near line rate; GC is not on the hot path for large streams | Best; zero-copy, io_uring crates | Best possible, at a high cost in effort | Poor without offloading everything to C extensions; GIL limits concurrency |
| Memory safety | Safe (GC) | Safe (borrow checker) | Unsafe; every parser is an attack surface | Safe |
| Concurrency model for 10k connections | goroutines + net/http: idiomatic and cheap | async + tokio: excellent but more ceremony | Manual (epoll/io_uring) or a framework | asyncio; weak for CPU-bound hashing |
| S3 ecosystem to borrow from | Largest: MinIO, SeaweedFS, versitygw, aws-sdk-go-v2, klauspost/reedsolomon, minio/sha256-simd, bbolt/pebble | Growing: RustFS, Garage, s3s, object_store, reed-solomon-simd | Ceph RGW (C++), little reusable outside Ceph | boto3, moto (mock only) |
| Time to first working product | Fastest for this domain; stdlib has HTTP/2, TLS, crypto, XML, JSON | 1.5–2x slower to write; slower compiles | Slowest; must build or vendor HTTP, TLS, XML, crypto layers | Fast to prototype, but not deployable as a serious store |
| Single static binary, cross-compile | Trivial (`GOOS=linux GOARCH=arm64 go build`) | Good (cross needs a target toolchain) | Painful (glibc/musl, per-target builds) | Not really |
| Contributor pool | Very large; most storage/infra tooling in this niche is Go | Large and enthusiastic; steeper learning curve | Smaller; expert-only | Huge, but not for this kind of system |
| Long-run performance ceiling | ~10–20% below Rust/C on CPU-bound paths; SIMD assembly available for hashing/EC | Highest practical | Highest | Lowest |

Assessment (my recommendation is Go; the trade-offs are):

1. **This exact product has been built in Go three times** (MinIO, SeaweedFS,
   Versity), which proves the language is adequate for multi-GB/s object
   storage and gives us battle-tested libraries for the hardest pieces:
   erasure coding (`klauspost/reedsolomon`, SIMD accelerated), highway hash
   for bitrot, SIMD SHA-256, embedded KV stores.
2. **The standard library covers the whole API surface**: HTTP/1.1 and
   HTTP/2 server, TLS, `crypto/*`, `encoding/xml`, `hash/crc32` with SSE4.2
   CRC32C. Rust needs a dozen crates for the same; C++ needs to pick and
   vendor a framework.
3. **Iteration speed matters more than the last 15% of CPU** for an
   S3 clone: the surface is ~100 API operations with thousands of edge cases,
   and being wire-compatible is the hard part, not raw speed. Disks and
   NICs, not the language runtime, bound object storage throughput.
4. **Static binaries and cross-compiling** are the deployment story users
   asked for in the "MinIO alternative" threads (single file, ARM NAS boxes,
   containers). Go makes this a one-liner.
5. Rust is the credible alternative and RustFS/Garage prove it. The costs are
   slower development and compile times, a smaller pool of storage-domain
   contributors, and (as RustFS's CVE-2025-68926 hard-coded gRPC token, CVSS 9.8, shows) Rust does not
   itself prevent logic bugs. If the project later needs a hot-path
   component in Rust (e.g. an erasure-coding or io_uring data plane) it can be
   linked in, but the control plane should stay Go.
6. C/C++ buys a small performance margin at a large safety and effort cost;
   Ceph RGW is the cautionary tale for complexity. Python is fine for the
   conformance test harness (the Ceph `s3-tests` suite is Python) but not
   for the server.

## What is bbolt?

bbolt is an embedded, pure-Go, single-file key/value database: a B+tree
with ACID transactions (one writer, many readers, MVCC via copy-on-write
pages, mmap for reads). It is the fork of Ben Johnson's BoltDB maintained
by the etcd team and is what etcd, Consul (historically) and many CNCF
tools use for their local state. It is comparable to LMDB.

We use it for **metadata only**: bucket records, per-version object
records, multipart bookkeeping, IAM users and policies. Object bytes never
go in it. Its properties fit the metadata problem well: ordered keys give us
prefix/delimiter listing with seeks; transactions give atomic
"write version record + update null-version pointer"; there is nothing to
operate (one file, no daemon). Its limitation is write throughput on a
single node (one writer at a time, fsync per commit) and file growth on very
large datasets, which is why it sits behind a five-method interface so it
can be replaced by pebble (RocksDB-style LSM, used by CockroachDB) for
write-heavy nodes, or by a distributed store in clustered mode.

---

# Amazon S3 Feature Survey (verified as of September 2026)

Method: every row below was checked against the current Amazon S3 User Guide / API Reference on docs.aws.amazon.com and against AWS "What's New" posts for 2024–2026. Dates are the AWS announcement dates unless noted. `[n]` markers refer to the source list at the end.

## 0. Orientation: bucket types, namespaces, and limits

| Feature | What it does | Notes / date |
|---|---|---|
| General purpose bucket | Classic flat-namespace bucket; all storage classes except Express One Zone; full feature set | API version 2006-03-01 |
| Directory bucket (S3 Express One Zone) | Zonal, hierarchical-namespace bucket; single-digit-ms latency; own endpoints and `CreateSession` auth | GA Nov 2023; up to 200k read / 100k write TPS per bucket [30][31] |
| Table bucket (S3 Tables) | Bucket type whose "objects" are Apache Iceberg tables; separate `s3tables` namespace | GA Dec 3, 2024; see §3 [4][17] |
| Vector bucket (S3 Vectors) | Bucket type holding vector indexes; separate `s3vectors` namespace | Preview Jul 15, 2025; GA Dec 2, 2025 [5][19] |
| Account regional namespace | Optionally scope general purpose bucket names to account+Region (`x-amz-bucket-namespace: account-regional`) | Mar 2026 [23] |
| Bucket quota | Default 10,000 buckets per account, raisable to 1,000,000 | Nov 14, 2024 [24] |
| Object size | Max object 50 TB; single PUT max 5 GB; multipart parts 5 MiB–5 GiB, max 10,000 parts | Raised from 5 TB Dec 2, 2025 [25][26] |
| Local Zones / Outposts | Directory buckets in Local Zones; Dedicated Local Zones; S3 on Outposts (`OUTPOSTS` class) | [2][6] |

## 1. Core API

### 1.1 Bucket, object, listing, multipart, copy

| Feature | What it does | Notes / date |
|---|---|---|
| Bucket CRUD | `CreateBucket` (LocationConstraint, Object Ownership, Object Lock enable, namespace, tags), `DeleteBucket`, `HeadBucket`, `GetBucketLocation` | — |
| ListBuckets | Paginated (`max-buckets`, `continuation-token`), `prefix` and `bucket-region` filters | Pagination added Nov 2024 [27] |
| ListDirectoryBuckets | Lists directory buckets | Nov 2023 |
| PutObject / GetObject / HeadObject / DeleteObject / DeleteObjects | Standard single-object operations; `DeleteObjects` up to 1,000 keys; `x-amz-meta-*`; storage-class, SSE, tagging, Object Lock headers on PUT | — |
| ListObjectsV2 (and legacy ListObjects) | `prefix`, `delimiter`, `max-keys`, `continuation-token`, `start-after`, `fetch-owner`, `encoding-type`; `x-amz-optional-object-attributes: RestoreStatus`; response includes `ChecksumAlgorithm`, `ChecksumType` | [28] |
| ListObjectVersions | Lists versions and delete markers | — |
| Multipart upload | Create/UploadPart/UploadPartCopy/Complete (conditional, checksum, `x-amz-mp-object-size`)/Abort/ListMultipartUploads/ListParts; ETag = MD5-of-MD5s + `-N` | — |
| CopyObject | Server-side copy ≤ 5 GB per request; metadata/tagging directive; `x-amz-copy-source-if-*`; `x-amz-annotation-directive` | Conditional-write headers on copy Oct 2025 [8] |
| RenameObject | Atomic in-place rename in directory buckets, with conditional headers and `x-amz-client-token` | Jun 2025; Express One Zone only [29] |
| Append (Express One Zone) | `PutObject` with `x-amz-write-offset-bytes`; ≤ 5 GB per append; 10,000-append cap | Nov 2024 [22] |
| Byte ranges | `Range` on GET/HEAD; `partNumber` to fetch an individual part | — |
| GetObjectAttributes | ETag, Checksum (all algorithms + type), ObjectParts (paginated), StorageClass, ObjectSize | [33] |
| RestoreObject | Restore from Glacier classes / IT archive tiers; Expedited/Standard/Bulk tiers; `x-amz-restore` status | `Type=SELECT` deprecated [34] |
| SelectObjectContent | SQL over one CSV/JSON/Parquet object | **Closed to new customers since Jul 25, 2024** [35] |
| CreateSession | Temporary session credentials for directory-bucket zonal operations | Nov 2023 |
| WriteGetObjectResponse | Object Lambda response API | Object Lambda closed to new customers Nov 7, 2025 [36] |
| GetObjectTorrent | Legacy BitTorrent retrieval | Retired feature |
| UpdateObjectEncryption | Re-wrap an object's data key (SSE-S3→SSE-KMS, key rotation, Bucket Keys) with no data movement; ETag/checksums preserved | Feb 5, 2026 [37] |
| Object Annotations | Put/Get/List/DeleteObjectAnnotation: up to 1,000 named payloads of ≤1 MiB per object version; conditional on parent ETag; replicate; emit events; queryable via Metadata annotation table | Jun 16, 2026 [38][39] |
| Bucket ABAC | `PutBucketAbac`/`GetBucketAbac` enable tag-based access control on general purpose buckets | Nov 20, 2025 [40] |
| Bucket sub-resources | ACL, policy, policy status, CORS, website, versioning, lifecycle, replication, logging, notification, request-payment, tagging, encryption, ownership controls, public access block, accelerate, metrics/analytics/inventory/intelligent-tiering, metadata configuration | See §1.5 |

### 1.2 Conditional requests

| Feature | What it does | Notes / date |
|---|---|---|
| Conditional reads | `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` on GET/HEAD (304/412); `x-amz-copy-source-if-*` | Long-standing [9] |
| `If-None-Match: *` on writes | Fails PutObject/CompleteMultipartUpload/CopyObject with 412 if key exists; 409 ConditionalRequestConflict on concurrent delete | Aug 20, 2024 (PUT/MPU); copy Oct 2025 [7][8][10] |
| `If-Match: <ETag>` on writes | Succeeds only if current ETag matches; 412 mismatch, 404 missing | Nov 25, 2024 [7] |
| Enforcing conditional writes | Condition keys `s3:if-match`, `s3:if-none-match` | Nov 25, 2024 [7] |
| Conditional deletes | `If-Match: <ETag>` or `*` on DeleteObject; per-object ETag in DeleteObjects body | Sep 16, 2025 [11] |
| Conditional rename / annotation writes | Source/destination conditions on RenameObject; `x-amz-object-if-match` on annotations | 2025 / 2026 |

### 1.3 Presigned URLs and browser POST

| Feature | What it does | Notes / date |
|---|---|---|
| Presigned URLs | SigV4-signed URLs for any object operation; expiry ≤ 7 days; `s3:signatureAge` condition | [41] |
| POST Object | HTML-form upload with base64 policy + signature; conditions (`starts-with`, `content-length-range`, ACL, SSE, tagging, storage class, metadata, `success_action_redirect`) | [42] |
| SigV4 / SigV4A | SigV4 everywhere (SigV2 retired); SigV4A for Multi-Region Access Points; streaming payload modes `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`, `-TRAILER`, `STREAMING-UNSIGNED-PAYLOAD-TRAILER` | [12] |

### 1.4 Checksums and integrity

| Feature | What it does | Notes / date |
|---|---|---|
| Content-MD5 / ETag | ETag is MD5 only for single-part plaintext/SSE-S3 objects | — |
| Additional checksum algorithms | CRC32, CRC32C, SHA1, SHA256 (Feb 2022); CRC64NVME (Dec 1, 2024); MD5, SHA512, XXHash64/3/128 (Apr 23, 2026): ten algorithms | [12][13][14] |
| Full-object vs composite | `x-amz-checksum-type: FULL_OBJECT | COMPOSITE`; full-object MPU checksums only for CRC family | Dec 2024 [12] |
| Default integrity protections | SDKs send CRC64NVME (or CRC32) by default; S3 computes and stores full-object CRC64NVME if none sent | Dec 2024 onward [12] |
| Trailing checksums | `aws-chunked` bodies with `x-amz-trailer`, `x-amz-decoded-content-length` | [12] |
| Checksum on download / at rest | `x-amz-checksum-mode: ENABLED`; per-part checksums via GetObjectAttributes/ListParts; Batch Operations "Compute checksum" job | 2025 [13] |

### 1.5 Complete API operation inventory

**Amazon S3 data plane (2006-03-01): 116 operations** [1]

- Objects: PutObject, GetObject, HeadObject, DeleteObject, DeleteObjects, CopyObject, RenameObject, RestoreObject, SelectObjectContent, GetObjectAttributes, GetObjectTorrent, UpdateObjectEncryption, WriteGetObjectResponse, POST Object.
- Multipart: CreateMultipartUpload, UploadPart, UploadPartCopy, CompleteMultipartUpload, AbortMultipartUpload, ListMultipartUploads, ListParts.
- Listing: ListObjects, ListObjectsV2, ListObjectVersions, ListBuckets, ListDirectoryBuckets.
- Object metadata/ACL/tags/lock: Get/PutObjectAcl, Get/Put/DeleteObjectTagging, Get/PutObjectLegalHold, Get/PutObjectRetention, Get/PutObjectLockConfiguration.
- Annotations (2026): Put/Get/List/DeleteObjectAnnotation.
- Buckets: CreateBucket, DeleteBucket, HeadBucket, GetBucketLocation, CreateSession.
- Bucket access/security: Get/PutBucketAcl, Get/Put/DeleteBucketPolicy, GetBucketPolicyStatus, Get/Put/DeletePublicAccessBlock, Get/Put/DeleteBucketOwnershipControls, Get/PutBucketAbac, Get/Put/DeleteBucketEncryption.
- Bucket configuration: Get/PutBucketVersioning, Get/PutBucketLifecycle (deprecated), Get/PutBucketLifecycleConfiguration, DeleteBucketLifecycle, Get/Put/DeleteBucketReplication, Get/Put/DeleteBucketCors, Get/Put/DeleteBucketWebsite, Get/PutBucketLogging, Get/PutBucketNotification (legacy), Get/PutBucketNotificationConfiguration, Get/PutBucketRequestPayment, Get/Put/DeleteBucketTagging, Get/PutBucketAccelerateConfiguration.
- Analytics/metrics/inventory/tiering: Get/Put/Delete/List for BucketAnalyticsConfiguration, BucketMetricsConfiguration, BucketInventoryConfiguration, BucketIntelligentTieringConfiguration.
- S3 Metadata: Create/Get/DeleteBucketMetadataTableConfiguration (V1, Jan 2025); Create/Get/DeleteBucketMetadataConfiguration, UpdateBucketMetadataJournalTableConfiguration, UpdateBucketMetadataInventoryTableConfiguration (V2, Jul 2025), UpdateBucketMetadataAnnotationTableConfiguration (Jun 2026).

**S3 Control (s3control): 97 operations** [15] covering Access Points, Object Lambda access points, Multi-Region Access Points, Batch Operations jobs, Storage Lens, Access Grants, account-level Block Public Access, Outposts buckets, resource tagging.

**S3 Tables (s3tables): 49 operations** [17] covering table buckets, namespaces, tables, policies, encryption, maintenance, replication, storage class, record expiration, tags, plus an Iceberg REST catalog endpoint [43].

**S3 Vectors (s3vectors): 19 operations** [18]: vector buckets, indexes, Put/Get/Delete/List/QueryVectors, policies, tags.

## 2. Storage classes, tiering, and lifecycle

| Class | What it does | Notes |
|---|---|---|
| STANDARD | Default, 11 nines, ≥3 AZs | — |
| EXPRESS_ONEZONE | Single-AZ, single-digit ms, directory buckets only | Price cuts Apr 2025 |
| INTELLIGENT_TIERING | Auto-tiers Frequent/Infrequent/Archive Instant, optional Archive/Deep Archive Access | — |
| STANDARD_IA / ONEZONE_IA | Infrequent access; 30-day minimum, 128 KB min billable | Day-0 transition allowed since Jul 16, 2026 [44] |
| GLACIER_IR / GLACIER / DEEP_ARCHIVE | Archive tiers with ms / hours / 12–48 h retrieval | — |
| REDUCED_REDUNDANCY | Legacy | Not recommended |
| OUTPOSTS, SNOW, FSX_*, AWS_BACKUP_* | Surfaced for edge/FSx/Backup access points | [33] |

Lifecycle [46][47]: rule actions Transition, Expiration (days/date/ExpiredObjectDeleteMarker), NoncurrentVersionTransition, NoncurrentVersionExpiration (NewerNoncurrentVersions), AbortIncompleteMultipartUpload; filters Prefix, Tag, And, ObjectSizeGreaterThan/LessThan; up to 1,000 rules; 128 KB default minimum for transitions (override header Sep 2024); directory buckets get expiration only; lifecycle events `s3:LifecycleExpiration:*`, `s3:LifecycleTransition`.

## 3. Data management

### 3.1 Versioning, Object Lock, replication, batch

| Feature | What it does | Notes / date |
|---|---|---|
| Versioning | Unversioned/Enabled/Suspended; version IDs, delete markers; required for Object Lock and replication | — |
| MFA Delete | MFA required to permanently delete versions or change versioning state | — |
| Object Lock | WORM per version; Governance (bypassable with permission) and Compliance modes; Legal hold; bucket default retention; can be enabled on existing buckets | [48] |
| Object Lock variable retention (event holds) | Retain-until fixed only when an event hold is released | Sep 2026 [49] |
| Live replication (CRR/SRR) | Async copy of new objects to one or many destinations; prefix/tag filters; delete-marker replication; replica modification sync; metrics | [50] |
| Replication Time Control | 99.99% within 15 min SLA | — |
| Batch Replication | On-demand replication of existing objects | 2022 |
| S3 Tables replication | Read-only Iceberg replicas | Dec 2, 2025 [51] |
| Batch Operations | Jobs over manifests or generated manifests (filters by date, key constraints, class, size, replication status, encryption); operations Copy, Compute checksum, Invoke Lambda, tags, ACL, Restore, Update encryption, Replicate, Lock retention/legal hold | Whole-bucket targeting Sep 2025; 20 B objects/job Dec 2025 [52][53] |

### 3.2 Discovery, analytics, and observability

| Feature | What it does | Notes / date |
|---|---|---|
| S3 Inventory | Scheduled CSV/ORC/Parquet listing with optional fields | Directory-bucket inventory Apr 2026 [54][55] |
| S3 Storage Lens | Org-wide analytics; free and advanced tiers; export to S3 Tables | Dec 2025 [56] |
| S3 Metadata | Managed Iceberg journal / live inventory / annotation tables | GA Jan 27, 2025; inventory Jul 2025; annotations Jun 2026 [57] |
| S3 Tables (Iceberg) | Table buckets with automatic compaction (binpack/sort/z-order), snapshot management, Iceberg REST endpoint, Glue integration, Iceberg V3 Variant type | GA Dec 2024; many additions through 2026 [17][58][59][45] |
| S3 Vectors | Vector buckets and indexes (1–4,096 dims, cosine/euclidean, metadata filters), top-K queries, 2 B vectors/index | GA Dec 2, 2025 [19][60] |
| Event notifications | SNS, SQS, Lambda, EventBridge; event types ObjectCreated:*, ObjectRemoved:*, ObjectRestore:*, Replication:*, LifecycleExpiration:*, LifecycleTransition, IntelligentTiering, ObjectTagging:*, ObjectAcl:Put, ObjectAnnotation:*, ObjectRetention:Put, TestEvent; prefix/suffix filters | System tags in events Jul 2026 [61][62] |
| Server access logging | Logs to a bucket, or (Jun 2026) to CloudWatch Logs / S3 Tables | [63] |
| CloudTrail / CloudWatch | Management and data events; storage, request and replication metrics | — |

### 3.3 Access and delivery features

| Feature | What it does | Notes / date |
|---|---|---|
| Access Points | Named endpoints with own policy, VPC-only origin, scopes | [64] |
| Multi-Region Access Points | Global endpoint, active-active/passive failover, SigV4A | [65] |
| Object Lambda | Lambda-transformed GET/HEAD/LIST | Closed to new customers Nov 7, 2025 [36] |
| Mountpoint for Amazon S3 | FUSE client, sequential writes, CSI driver | [66] |
| Amazon S3 Files | Managed NFS 4.1/4.2 file system over a bucket, mountable from EC2/ECS/EKS/Lambda | GA Apr 7, 2026 [67][68] |
| Storage Browser for S3 | Open-source Amplify UI component | Dec 2024 |
| Transfer Acceleration, Requester Pays, Static website hosting, CORS | Long-standing bucket features | Not for directory buckets |
| Object tagging | ≤10 tags per version; used in IAM, lifecycle, replication filters | — |
| Bucket / resource tags | Cost allocation and ABAC | — |
| Ingestion integrations | Kinesis Data Streams → buckets and S3 Tables (Aug 2026); AWS Backup | 2026 |

## 4. Security

| Feature | What it does | Notes / date |
|---|---|---|
| IAM identity policies | `s3:*`, `s3express:*`, `s3tables:*`, `s3vectors:*` actions; permissions boundaries | — |
| Bucket policies | Resource policies ≤ 20 KB; `x-amz-expected-bucket-owner` | — |
| Access point / table / vector bucket policies | Per-endpoint resource policies | — |
| Organizations SCPs and RCPs | Org-wide maximum permissions | RCPs Nov 2024 |
| Condition keys | `s3:prefix`, `s3:delimiter`, `s3:x-amz-acl`, `s3:x-amz-server-side-encryption(-aws-kms-key-id)`, `s3:x-amz-storage-class`, `s3:ExistingObjectTag/*`, `s3:RequestObjectTag/*`, `s3:object-lock-*`, `s3:signatureAge`, `s3:signatureversion`, `s3:authType`, `s3:TlsVersion`, `s3:ResourceAccount`, `s3:if-match`, `s3:if-none-match`, `s3:x-amz-bucket-namespace`, `aws:SecureTransport`, `aws:SourceVpc(e)`, `aws:ResourceTag/*` | [69] |
| ACLs and Object Ownership | `BucketOwnerEnforced` default since Apr 2023 (ACLs disabled); `BucketOwnerPreferred` / `ObjectWriter` legacy; canned and XML ACLs | [70] |
| Block Public Access | Four settings at access-point, bucket, account and organization level; on by default since Apr 2023 | Org-level Nov 26, 2025 [71] |
| IAM Access Analyzer | Findings for public/cross-account buckets | [70] |
| Encryption at rest | SSE-S3 default since Jan 2023; SSE-KMS with Bucket Keys; DSSE-KMS; SSE-C; client-side; `UpdateObjectEncryption` | [72] |
| SSE-C disabled by default | New buckets reject SSE-C unless enabled via PutBucketEncryption | Rolling out from Apr 6, 2026 [73] |
| Encryption in transit | TLS 1.2 minimum since Feb 2024; TLS 1.3; FIPS endpoints | [74] |
| VPC connectivity | Gateway and interface endpoints, endpoint policies, IPv6 dual-stack | — |
| STS / temporary credentials | AssumeRole, session tags, CreateSession, Access Grants GetDataAccess | — |
| S3 Access Grants | Grants to IAM principals or Identity Center users/groups; vends scoped credentials | GA Nov 2023 [75] |
| ABAC | Tag-based access on buckets, access points, directory buckets, Tables | 2025 |
| Monitoring | GuardDuty S3 Protection, Macie, Security Hub, Config rules | [70] |

## 5. What is new in 2025–2026 (chronological digest)

| Date | Change |
|---|---|
| Jan 27, 2025 | S3 Metadata GA |
| Jan 30, 2025 | S3 Tables schemas at create; 10,000 tables per bucket |
| Mar–May 2025 | Access points for directory buckets |
| Apr 2025 | Express One Zone price cuts; Tables SSE-KMS |
| Jun 2025 | RenameObject (Express); Tables sort/z-order compaction |
| Jul 15, 2025 | S3 Vectors preview; S3 Metadata V2 live inventory |
| Sep 15–16, 2025 | Batch Operations whole-bucket targeting; conditional deletes |
| Oct 2025 | Conditional writes on CopyObject |
| Nov 2025 | Bucket ABAC; org-level Block Public Access; Object Lambda closed to new customers; SSE-C default change notice |
| Dec 2, 2025 | S3 Vectors GA; 50 TB objects; Tables replication and Intelligent-Tiering; Batch Operations 10x; Storage Lens upgrades |
| Feb 5, 2026 | UpdateObjectEncryption |
| Mar 2026 | Account regional namespaces |
| Apr 2026 | S3 Files GA (NFS); SSE-C off by default; five new checksum algorithms |
| Jun 2026 | S3 Annotations; access logs to CloudWatch Logs / Tables |
| Jul 2026 | Iceberg V3 Variant; 30-day IA minimum removed; system tags in events |
| Aug 2026 | Kinesis delivery to buckets and Tables |
| Sep 2026 | Object Lock variable retention; PrivateLink FIPS endpoints |

Deprecations to note: S3 Select (no new customers since Jul 2024), Object Lambda (Nov 2025), legacy lifecycle/notification APIs, ListObjects v1 (kept), GetObjectTorrent, RRS, SigV2, TLS ≤ 1.1, SSE-C now opt-in.

## Sources

1. https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_Simple_Storage_Service.html
2. https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage-class-intro.html
3. https://docs.aws.amazon.com/AmazonS3/latest/userguide/WhatsNew.html
4. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables.html
5. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors.html
6. https://docs.aws.amazon.com/AmazonS3/latest/userguide/directory-buckets-overview.html
7. https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html
8. https://aws.amazon.com/about-aws/whats-new/2025/10/amazon-s3-conditional-write-functionality-copy-operations
9. https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-reads.html
10. https://aws.amazon.com/about-aws/whats-new/2024/08/amazon-s3-conditional-writes
11. https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-deletes.html
12. https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html
13. https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity.html
14. https://aws.amazon.com/about-aws/whats-new/2026/04/s3-five-additional-checksum-algorithms/
15. https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_AWS_S3_Control.html
17. https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_S3_Tables.html
18. https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_S3_Vectors.html
19. https://aws.amazon.com/about-aws/whats-new/2025/12/amazon-s3-vectors-generally-available/
22. https://docs.aws.amazon.com/AmazonS3/latest/userguide/directory-buckets-objects-append.html
23. https://docs.aws.amazon.com/AmazonS3/latest/userguide/gpbucketnamespaces.html
24. https://aws.amazon.com/about-aws/whats-new/2024/11/amazon-s3-up-1-million-buckets-per-aws-account/
25. https://aws.amazon.com/about-aws/whats-new/2025/12/amazon-s3-maximum-object-size-50-tb/
26. https://docs.aws.amazon.com/AmazonS3/latest/userguide/qfacts.html
27. https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBuckets.html
28. https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html
29. https://docs.aws.amazon.com/AmazonS3/latest/API/API_RenameObject.html
30. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-express-differences.html
31. https://docs.aws.amazon.com/AmazonS3/latest/userguide/directory-buckets-overview.html
33. https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html
34. https://docs.aws.amazon.com/AmazonS3/latest/API/API_RestoreObject.html
35. https://docs.aws.amazon.com/AmazonS3/latest/API/API_SelectObjectContent.html
36. https://docs.aws.amazon.com/AmazonS3/latest/userguide/transforming-objects.html
37. https://docs.aws.amazon.com/AmazonS3/latest/API/API_UpdateObjectEncryption.html
38. https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectAnnotation.html
39. https://docs.aws.amazon.com/AmazonS3/latest/userguide/annotations-overview.html
40. https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAbac.html
41. https://docs.aws.amazon.com/AmazonS3/latest/userguide/PresignedUrlUploadObject.html
42. https://docs.aws.amazon.com/AmazonS3/latest/API/RESTObjectPOST.html
43. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-integrating-open-source.html
44. https://aws.amazon.com/about-aws/whats-new/2026/07/s3-removes-30-day-transitions-standard-ia-one-zone-ia/
45. https://docs.aws.amazon.com/AmazonS3/latest/userguide/tables-intelligent-tiering.html
46. https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lifecycle-mgmt.html
47. https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-transition-general-considerations.html
48. https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html
49. https://aws.amazon.com/about-aws/whats-new/2026/09/amazon-s3-object-lock-variable-retention/
50. https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication.html
51. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-replication-tables.html
52. https://docs.aws.amazon.com/AmazonS3/latest/userguide/batch-ops-operations.html
53. https://aws.amazon.com/about-aws/whats-new/2025/12/s3-batch-operations-performance-improvements
54. https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage-inventory.html
55. https://aws.amazon.com/about-aws/whats-new/2026/04/s3-express-one-zone-supports-s3-inventory/
56. https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage_lens.html
57. https://docs.aws.amazon.com/AmazonS3/latest/userguide/metadata-tables-overview.html
58. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-maintenance-overview.html
59. https://aws.amazon.com/about-aws/whats-new/2026/07/amazon-s3-tables-variant-iceberg-v3/
60. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors-limitations.html
61. https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html
62. https://aws.amazon.com/about-aws/whats-new/2026/07/amazon-s3-event-notifications-system-generated-tags/
63. https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerLogs.html
64. https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-points.html
65. https://docs.aws.amazon.com/AmazonS3/latest/userguide/MultiRegionAccessPoints.html
66. https://docs.aws.amazon.com/AmazonS3/latest/userguide/mountpoint.html
67. https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-files.html
68. https://aws.amazon.com/about-aws/whats-new/2026/04/amazon-s3-files/
69. https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazons3.html
70. https://docs.aws.amazon.com/AmazonS3/latest/userguide/security-best-practices.html
71. https://aws.amazon.com/about-aws/whats-new/2025/11/amazon-s3-block-public-access-organization-level-enforcement
72. https://docs.aws.amazon.com/AmazonS3/latest/userguide/serv-side-encryption.html
73. https://aws.amazon.com/about-aws/whats-new/2026/04/s3-default-bucket-security-setting/
74. https://aws.amazon.com/blogs/security/tls-1-2-required-for-aws-endpoints/
75. https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-grants.html

---

# MinIO: commercialisation, feature reduction and current state (verified 12 September 2026)

Method: checked against primary sources on 12 Sept 2026 (GitHub API and pages, min.io blog and docs, Docker Hub and Quay APIs, dl.min.io, Hacker News, Wayback CDX) plus secondary reporting.

## Timeline

### Licence change: Apache-2.0 to AGPLv3 (April–May 2021)

| Date | Event | Source |
|---|---|---|
| 23 Apr 2021 | Commit `0694325` swaps the root LICENSE to AGPLv3 across 977 files with no prior announcement | [discussion #12157](https://github.com/minio/minio/discussions/12157), [issue #12155](https://github.com/minio/minio/issues/12155) |
| 11 May 2021 | Server, `mc` and gateway complete the move to AGPLv3; client SDKs stay Apache-2.0 | [min.io blog](https://www.min.io/blog/from-open-source-to-free-and-open-source-minio-is-now-fully-licensed-under-gnu-agplv3) |
| 2022–2023 | MinIO accuses Nutanix and Weka of licence violations, "revoking" Weka's licence | [Blocks & Files](https://blocksandfiles.com/2023/03/26/we-object-minio-says-no-more-open-license-for-you-weka/) |

### Gateway, NAS and FS-mode removal (2022)

| Date | Event | Source |
|---|---|---|
| Feb 2022 | "Deprecation of the MinIO Gateway": NAS, S3, Azure, GCS and HDFS gateways to be removed within six months; gateway-only users were "<2% of commercial customers" | [min.io blog](https://min.io/blog/deprecation-of-the-minio-gateway) |
| 29 Oct 2022 | Release removes remaining gateway implementations and legacy FS mode; single-drive deployments move to the 0-parity erasure-coded format | [release notes](https://github.com/minio/minio/releases/tag/RELEASE.2022-10-29T06-21-33Z) |

### Enterprise Object Store and AIStor (2024)

| Date | Event | Source |
|---|---|---|
| 2024 | Commercial edition adds Catalog (metadata search), Firewall, KMS, Cache, Observability, Global Console | [aistor-overview](https://www.min.io/blog/aistor-overview) |
| 13 Nov 2024 | AIStor launched: promptObject API, AIHub, S3 over RDMA, new console and operator | [press release](https://www.min.io/press/minio-releases-aistor-purpose-built-for-ai-and-data-workloads) |

### Console gutted in the community edition (Feb–Jun 2025)

| Date | Event | Source |
|---|---|---|
| 26 Feb 2025 | PR #3509 in `minio/object-browser` removes accounts, policies, bucket management, configuration, lifecycle, tiers, site replication, monitoring and IDP login from the community console | [Blocks & Files, 19 Jun 2025](https://www.blocksandfiles.com/ai-ml/2025/06/19/minio-users-complain-after-admin-ui-removed-from-community-edition/1610856) |
| 22 Apr 2025 | `RELEASE.2025-04-22T22-12-26Z`: last server release with the full admin console (console 1.7.6) | [release notes](https://github.com/minio/minio/releases/tag/RELEASE.2025-04-22T22-12-26Z) |
| 24 May 2025 | `RELEASE.2025-05-24T17-08-30Z`: "Embedded UI Console is now deprecated... External IDP logins via LDAP/OIDC are removed as well; these are now available as part of the AiStor Product." | [release notes](https://github.com/minio/minio/releases/tag/RELEASE.2025-05-24T17-08-30Z) |
| 26–27 May 2025 | Discussions [#21316](https://github.com/minio/minio/discussions/21316) and [#21326 "It's not a feature issue, it's a trust one"](https://github.com/minio/minio/discussions/21326), the latter locked by the co-founder | GitHub |
| 30 May 2025 | HN "MinIO Removes Web UI Features from Community Version" 176 points | [HN](https://news.ycombinator.com/item?id=44136108) |
| 19 Jun 2025 | `minio/kes` (open-source KMS bridge) archived; replacement is the commercial Key Manager | [github.com/minio/kes](https://github.com/minio/kes) |

Reported AIStor entry price at the time: about $96,000 per year for up to 400 TiB.

### Documentation pulled, binaries and images stopped (Oct 2025)

| Date | Event | Source |
|---|---|---|
| 10 Oct 2025 | Community documentation pulled from web hosting; "No further development of the documentation is planned" | [minio/docs README](https://github.com/minio/docs/blob/main/README.md) |
| 15–16 Oct 2025 | `RELEASE.2025-10-15T17-29-55Z` fixes CVE-2025-62506 (CVSS 8.1, session-policy bypass). Last release ever; published with no binary assets | [releases](https://github.com/minio/minio/releases) |
| 18 Oct 2025 | Issue #21647 asking for a Docker image closed "working as intended": "MinIO currently only distributes source code" | [issue #21647](https://github.com/minio/minio/issues/21647) |
| 22–23 Oct 2025 | HN "MinIO stops distributing free Docker images": 733 points, 555 comments | [HN](https://news.ycombinator.com/item?id=45665452) |

State of distribution on 12 Sept 2026: dl.min.io returns HTTP 410 Gone for server and client binaries; Docker Hub `minio/minio` and `minio/mc` repositories were deleted in the week of 7–12 Sept 2026 (downstream breakage issues opened that day, e.g. [OpenCTI #18242](https://github.com/OpenCTI-Platform/opencti/issues/18242)); quay.io still serves frozen tags, latest community tag `RELEASE.2025-09-07T16-13-09Z`, which is unpatched for CVE-2025-62506.

### "Maintenance mode" (Dec 2025)

Commit `27742d4` by the co-founder on 3 Dec 2025 inserted into the README: "This project is currently under maintenance and is not accepting new changes." No new features or pull requests; critical security fixes "evaluated on a case-by-case basis"; existing issues not reviewed. No blog post. HN ["MinIO is now in maintenance-mode"](https://news.ycombinator.com/item?id=46136023): 511 points, 322 comments. In practice: no code commits after 3 Dec 2025 and no security release after Oct 2025.

### "No longer maintained", archived (2026)

| Date | Event | Source |
|---|---|---|
| 6 Jan 2026 | README recommends "AIStor Free" as replacement | commit list |
| 12 Feb 2026 | README heading: "THIS REPOSITORY IS NO LONGER MAINTAINED"; repo archived 13 Feb; HN 500 points | [HN](https://news.ycombinator.com/item?id=47000041) |
| Feb–Apr 2026 | Repo briefly unarchived, reason unstated | [It's FOSS](https://itsfoss.com/news/minio-moves-away-from-open-source/) |
| 25 Apr 2026 | `minio/minio` archived, final | [github.com/minio/minio](https://github.com/minio/minio) |

State on 12 Sept 2026: archived, 61,365 stars, 7,871 forks, 80 open issues, last release `RELEASE.2025-10-15T17-29-55Z` (source only), licence AGPLv3.

### Satellite projects

| Repo | Status |
|---|---|
| `minio/mc` | Archived 14 Jul 2026; last release Aug 2025 |
| `minio/operator` | Archived 20 Mar 2026; AIStor has a proprietary operator with a one-way upgrade |
| `minio/object-browser` (ex-console) | Repository no longer exists on GitHub (404 by Jun 2026); source survives only in forks |
| `minio/kes` | Archived Jun 2025 |
| `minio/docs` | Hosting pulled Oct 2025 |
| Client SDKs (minio-go, -java, -py, -js) | Still maintained, Apache-2.0, because AIStor customers use them |

### AIStor in 2026

AIStor Tables GA (Iceberg V3 REST catalog embedded, Feb 2026), NVIDIA BlueField and GPUDirect RDMA for S3 (Mar 2026), MemKV context store (May 2026), AIStor Memory for agents (Jul 2026). TuxCare launched paid Endless Lifecycle Support for the archived community edition in May 2026.

## Feature matrix

Columns: A = last fully featured community release (Apr 2025); B = final community edition (Oct 2025, archived; what you get from `master` or Silo); C = AIStor.

The 2025 removals were almost entirely UI, identity login, distribution and support. The server-side feature set in B equals A. AIStor is a superset with proprietary add-ons.

| Feature | A: CE Apr 2025 | B: CE final | C: AIStor |
|---|---|---|---|
| Erasure coding (Reed-Solomon, per-object parity) | Yes | Yes | Yes (Free: single node only) |
| Bitrot protection (HighwayHash) | Yes | Yes | Yes |
| Healing | Yes | Yes | Yes |
| Distributed multi-pool, rebalance | Yes | Yes | Enterprise only |
| Site replication | Yes, in console | `mc admin` only | Enterprise |
| Bucket replication (active-active, multi-target) | Yes | Yes | Yes |
| Versioning | Yes | Yes | Yes |
| Object lock, legal hold, retention | Yes | Yes | Yes |
| Lifecycle (expiry, noncurrent, delete-marker cleanup) | Yes | Yes | Yes (Free excludes transitions) |
| Tiering to remote S3/Azure/GCS + RestoreObject | Yes | Yes | Enterprise only |
| IAM users, groups, policies, service accounts | Console + mc | mc only | Yes + Global Console |
| STS (AssumeRole, WebIdentity, LDAPIdentity, Certificate) | Yes | Yes | Yes |
| OIDC / LDAP | Yes incl. console SSO | Server-side only; console login removed | Yes |
| SSE-S3, SSE-KMS, SSE-C via KES | Yes | Yes, KES archived | Proprietary Key Manager |
| Notification targets | AMQP, MQTT, NATS, NSQ, Elasticsearch, Kafka, MySQL, PostgreSQL, Redis, Webhook | Same | Same |
| S3 Select | Yes | Yes | Yes |
| Batch jobs (replicate, keyrotate, expire) | Yes | Yes | Yes |
| Console | Full admin console | Object browser only | Global Console |
| Prometheus metrics v2/v3 | Yes | Yes | Yes + Observability |
| Audit logging (webhook, Kafka) | Yes | Yes | Yes |
| Admin API / `mc admin` | Yes | Yes (`mc` archived) | Yes |
| Catalog / metadata search | No | No | Enterprise |
| Bucket quotas | Yes | Yes | Yes |
| Compression, throttling, FTP/SFTP | Yes | Yes | Yes |
| S3 Tables / Iceberg | No | No | AIStor Tables |
| S3 Express directory buckets, append | No | No | Yes |
| Conditional writes (If-Match/If-None-Match on PUT) | Yes | Yes | Yes |
| S3 over RDMA, GPUDirect | No | No | Yes |
| Kubernetes operator | minio/operator (archived) | Unmaintained | Proprietary |
| Distribution | Binaries, Docker Hub, Quay, RPM/DEB | Source only | Licensed via SUBNET |
| Licence | AGPLv3 | AGPLv3, archived | Proprietary (Free single-node / Enterprise) |

AIStor Free exclusions: multi-node, replication, tiering and lifecycle transitions, version-specific deletion, profiling and diagnostics, telemetry and console logs, heal status, support commands. It is not a like-for-like replacement for the community edition's distributed mode.

## Community reaction and forks

HN threads in order of size: Docker images stopped (733 points), maintenance mode (511), no longer maintained (500), console removal (176). MinIO closed or locked GitHub threads quickly.

| Fork | What | Status on 12 Sept 2026 |
|---|---|---|
| **Silo** (formerly pgsty/minio), [github.com/pgsty/silo](https://github.com/pgsty/silo) | Full server fork by the Pigsty maintainer from 25 Oct 2025; console reverted to 1.7.6 with OIDC/LDAP login, SUBNET and telemetry stripped, binaries, RPM/DEB/APK, multi-arch images, SBOM and Sigstore signatures; keeps S3 API, `MINIO_*` env vars, on-disk format | Active: 2,784 stars; releases every 1–2 months, latest `RELEASE.2026-09-03`; 14 security fixes with advisories; adopted by RAGFlow, Dokploy, Grafana Loki, nixpkgs, OpenCTI; renamed Aug 2026 to avoid trademark exposure |
| OpenMaxIO | Console-only fork, May 2025 | Dormant since Jun 2025 |
| eithan1231/minio-object-browser | Console fork | Archived Dec 2025; description now says "avoid using ANY minio products" |
| minio.community | Mirror of pulled docs | Online |
| Chainguard, Minimus, others | Image rebuilds, not forks | No backports except Silo |

Where the community actually went: Garage (NixOS moved its tests to it and plans to drop the minio package), SeaweedFS (Kubeflow Pipelines switched its default object store to it), RustFS, Ceph RGW, Versity. InfoQ noted in Dec 2025 that "no fork of the MinIO community edition has gained traction"; the fork that stuck (Silo) came from outside the original advocates.

## S3 features MinIO never supported

From MinIO's own limits document and compatibility pages:

- Bucket-level: `PutBucketAcl`/`GetBucketAcl`, `PutBucketCors`/`DeleteBucketCors` (CORS enabled globally; Silo added per-bucket CORS in Sept 2026), `PutBucketWebsite`, bucket logging, Inventory, Metrics and Analytics configurations, Accelerate, Requester Pays, PublicAccessBlock, OwnershipControls, Intelligent-Tiering, `CreateSession`/directory buckets (CE).
- Object-level: `GetObjectAcl`/`PutObjectAcl`; `DeleteObject(s)` ignores `If-Match`; `ListMultipartUploads` requires the exact key as prefix; `AbortIncompleteMultipartUpload` lifecycle action unsupported.
- **Conflicting keys**: an object `a/b` and an object `a/b/1.txt` cannot coexist. MinIO called this a "broken feature from AWS S3" and marked it wontfix ([discussion #13641](https://github.com/minio/minio/discussions/13641)). This is the incompatibility most likely to bite migrating applications.
- Object names constrained by the host filesystem; AWS storage classes other than STANDARD/REDUCED_REDUNDANCY not honoured.
- Whole AWS areas absent: Batch Operations API (MinIO has its own `mc batch`), Access Points, Object Lambda, Storage Lens, Multi-Region Access Points, S3 Metadata, and in the CE S3 Tables and Express One Zone.

## Bottom line

The AGPLv3 MinIO codebase is frozen and read-only with no official binary, image or download channel. The last official image is unpatched for CVE-2025-62506. The only actively maintained continuation is Silo. MinIO Inc. sells AIStor and invests in Iceberg tables, S3 Express, RDMA and AI-memory products, none of which exist in the open code. The archived community edition still has the complete server capability set; what it lacks is the admin UI, IDP console login, distribution, hosted docs and any maintenance.

---

# Open-source and other S3-compatible alternatives (verified 12 September 2026)

Method: figures were checked against primary sources on 12 Sept 2026 (GitHub API for stars, licence and push dates; project docs for feature matrices; release pages for dates). Reddit could not be fetched directly, so "community wants" draws on Hacker News threads, the MinIO GitHub discussion, the Cloudron forum and 2026 articles summarising r/selfhosted and r/homelab.

## RustFS

| Field | Value |
|---|---|
| Language / licence | Rust / Apache-2.0 |
| GitHub | 32.0k stars, created Nov 2023, last push 2026-09-12 |
| Latest release | 1.0.0-rc.6, 2026-09-11, still a prerelease; GA was targeted for July 2026 and has slipped |
| Maintainer / business | Open-sourced July 2025; Chinese sources name Beijing Hengsha Technology as initiator; enterprise support via contact form, no separate edition; new features paused to focus on stability ([issue #1097](https://github.com/rustfs/rustfs/issues/1097)) |
| Deployment | Single node and multi-node multi-disk erasure coding (Reed-Solomon), pool expansion, Helm chart, early operator (v0.0.6) |
| S3 coverage | README claims versioning, object lock, SSE-S3/KMS/C, IAM, S3 Select, bucket and site replication, lifecycle, audit logging, notifications; S3 Tables preview. API surface comes from a pinned fork of the `s3s` crate. No independent conformance matrix published. MinIO on-disk compatibility behind a non-default feature flag; MinIO-encrypted objects unreadable. |
| IAM | MinIO-style users, policies, service accounts, STS, OIDC, Keystone, mTLS |
| Encryption | SSE-S3/KMS/C; Vault and AWS KMS backends |
| Admin UI | Web console shipped |
| Extras | FTPS, WebDAV, SFTP, Swift API, MCP server, migration from Azure/GCS |
| Strengths | Closest visual and operational MinIO replacement; Apache-2.0; fast cadence |
| Weaknesses | Security record: [CVE-2025-68926](https://github.com/advisories/GHSA-h956-rh7x-ppgj) hard-coded gRPC token (CVSS 9.8), CVE-2025-68705 path traversal (9.9), CVE-2025-69255 DoS, CVE-2026-21862 SourceIp bypass via X-Forwarded-For, and ten-plus advisories Jun–Sep 2026 including IAM condition-evaluation bugs and object-lock protections treated as absent when bucket metadata is unreadable ([advisories](https://github.com/rustfs/rustfs/security/advisories)). Hacker News reactions: suspected astroturfing, AI-generated announcements, copyright-assignment CLA, weekly releases seen as an anti-feature for storage ([HN](https://news.ycombinator.com/item?id=47439450)). |
| Pitch | Drop-in MinIO replacement in memory-safe Rust, Apache-2.0 "no poison pill", AI and data-lake positioning |

## Garage (Deuxfleurs)

| Field | Value |
|---|---|
| Language / licence | Rust / AGPL-3.0 |
| GitHub | 4.5k stars (mirror), last push 2026-09-12 |
| Latest release | v2.4.1 (2026-09-08) |
| Maintainer / business | Deuxfleurs, French non-profit; publicly funded (NGI, NLnet); no commercial edition |
| Deployment | Single node to geo-distributed; replication only (typically 3 copies across zones); erasure coding is an explicit non-goal; eventual consistency via CRDTs, no consensus; ~1 GB RAM minimum |
| S3 coverage | Yes: SigV4, path and vhost, presigned, PostObject, multipart incl. UploadPartCopy, SSE-C, website, CORS, lifecycle (expiration and abort-MPU only). No: versioning, object lock, bucket policies, ACLs, tagging, notifications, replication API, SSE-S3/KMS, Select, storage classes ([compat page](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/)) |
| IAM | Own key model with per-bucket read/write/owner grants; no AWS IAM policies; no OIDC |
| Admin UI | None official; NLnet-funded UI project unshipped as of v2.4; third-party UIs exist |
| Strengths | Rock-solid reputation, tiny footprint (Raspberry Pi, ~50 MB binary), WAN-tolerant, honest docs; most-recommended homelab replacement |
| Weaknesses | No versioning or object lock (blocks Veeam-style immutable backups), no policies or tags, no erasure coding (3x storage cost), AGPL, ~5 Gbit/s reported ceiling, no official UI |
| Pitch | "An S3 object store so reliable you can run it outside datacenters" |

## SeaweedFS

| Field | Value |
|---|---|
| Language / licence | Go / Apache-2.0 |
| GitHub | 34.6k stars, last push 2026-09-12 |
| Latest release | 4.46 (2026-09-08) |
| Maintainer / business | Chris Lu, now full-time. 2025–26 commercial move: "SeaweedFS Enterprise" licence key at $2/TB/month; enterprise-only features include customisable EC ratio, zstd compression, point-in-time recovery, self-healing EC repair, EC bitrot scrub, admin-UI OIDC, multi-tenancy, S3 QoS, kernel mount, Iceberg table maintenance ([pricing](https://seaweedfs.com/docs/pricing/)) |
| Deployment | Master + volume servers + filer + S3 gateway (separate processes or `weed server`); replication for hot data, 10+4 Reed-Solomon for warm read-only volumes; pluggable filer metadata store; operator and CSI driver |
| S3 coverage | Yes: versioning, object lock, legal hold, multipart, SSE-S3/C/KMS, ACLs, bucket policies with conditions, presigned and POST, conditional headers, CORS, tagging, lifecycle (no transition). No: S3 event notifications, website hosting, replication API, Select, analytics, inventory, logging config, MFA delete ([wiki](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)) |
| IAM | IAM users, groups, policies, STS with OIDC/LDAP/Kubernetes service accounts |
| Admin UI | Open-source admin UI; OIDC login enterprise-only |
| Strengths | Mature since 2012, billions of small files, broadest feature set (FUSE, WebDAV, HDFS, Iceberg), strong raw performance, Apache-2.0 |
| Weaknesses | Multi-component architecture; memory issues and OOMs reported in 2026 releases; "be ready to understand their file format to fix corruption by hand"; single-maintainer bus factor; new open-core boundary |
| Pitch | Object storage, file system and Iceberg tables for billions of files; enterprise is a licence key |

## Ceph RGW

| Field | Value |
|---|---|
| Language / licence | C++ / LGPL-2.1 or LGPL-3 |
| GitHub | 17.0k stars |
| Latest release | Tentacle v20.2.4 (2026-08-19) |
| Maintainer / business | Ceph Foundation (Linux Foundation); IBM/Red Hat, Clyso, Canonical, SUSE sell support |
| Deployment | Distributed only (MON/OSD/MGR + RGW); replication or erasure-coded pools; multisite; cephadm and Rook |
| S3 coverage | Most complete outside AWS: versioning, lifecycle, policies and ACLs, website, notifications (Kafka/AMQP/HTTP), tagging, storage classes, bucket logging, PublicAccessBlock, POST, GetObjectAttributes, object lock, SSE-C/KMS/S3, S3 Select, CORS ([table](https://docs.ceph.com/en/latest/radosgw/s3/)) |
| IAM | Accounts with IAM API (users, groups, roles, policies, STS) since Squid; Keystone and LDAP |
| Admin UI | Ceph Dashboard |
| Strengths | Exabyte-proven; unified block, file and object; foundation governance |
| Weaknesses | Heavy (3–5 nodes minimum), "far from simple to operate", not WAN-friendly, overkill below rack scale |

## Apache Ozone

| Field | Value |
|---|---|
| Language / licence | Java / Apache-2.0; 1.3k stars; 2.2.1 (2026-08-27) |
| Maintainer | ASF; Cloudera principal contributor |
| Deployment | Distributed (OM, SCM, datanodes, S3 gateway); replication or EC |
| S3 coverage | Bucket and object CRUD, ListObjectsV2, multipart, tagging, presigned URLs. No versioning, object lock, SSE headers, Select, conditional requests, ACLs, policies, CORS, website, lifecycle, replication, notifications |
| IAM | Kerberos secrets; Apache Ranger authorisation |
| Weaknesses | JVM-heavy, many moving parts, thin S3 surface, small community outside Cloudera |

## Zenko CloudServer (Scality)

JavaScript / Apache-2.0, 1.9k stars, 9.0.32-3 (2026-09-03). Single-node file backend for dev or multi-cloud gateway; lifecycle, replication, notifications and object lock live in the heavier Zenko stack (Backbeat, MongoDB, Redis). Niche community, dated docs.

## Versity S3 Gateway (versitygw)

Go / Apache-2.0, 2.8k stars, v1.8.0 (2026-09-04) with a standalone IAM service, STS web identity, policy conditions, WebGUI. Stateless gateway over POSIX, ScoutFS, S3 or Azure; no redundancy of its own; versioning experimental via a side directory; notifications to Kafka/NATS/webhook; no object ACLs, lifecycle, replication or SSE documented. Strength: objects are files with metadata in xattrs. Pitch: "S3 for the filesystem you already have".

## Storj

Go; storj/storj AGPL, gateway-st Apache-2.0. Not self-hostable as a store. Storj Labs filed Chapter 11 on 26 July 2026 ([CoinDesk](https://www.coindesk.com/business/2026/07/27/cloud-data-firm-storj-files-for-chapter-11-extending-a-week-of-crypto-failures-token-slides-16)); token delisted by Binance Sept 2026. Corporate risk dominates.

## OpenIO and OpenStack Swift

OpenIO was acquired by OVH in 2020 and withdrawn; its repo is effectively OVH-internal. Swift s3api middleware (Python, Apache-2.0, 2.37.3 Aug 2026) supports CRUD, multipart, ACLs, versioning; no policies, lifecycle, notifications, website or tagging; needs ~8 GB RAM and OpenStack.

## Test and mock servers, frameworks

| Project | Facts |
|---|---|
| LocalStack | 65k stars, Python. Community Edition ended 23 Mar 2026; the image now requires an account and the free tier is non-commercial ([blog](https://blog.localstack.cloud/the-road-ahead-for-localstack/)) |
| Moto | 8.7k stars, Python, Apache-2.0; versioning, object lock, multipart, policies (no Conditions), lifecycle, notifications, tags, CORS, website, replication config |
| adobe/S3Mock | 1.1k stars, Kotlin; versioning, lock, tagging, lifecycle; no policies, CORS, SSE; presigned not validated |
| s3s (Rust) | 306 stars, Apache-2.0; generic S3 service generated from the AWS Smithy model; used by RustFS via a pinned fork |
| Riak CS | Basho repo archived 2024; TI Tokyo fork on life support |

## JuiceFS gateway and Lakekeeper

JuiceFS (Go, Apache-2.0, 14.4k stars) exposes S3 via a pre-licence-change MinIO fork; needs Redis/TiKV plus an object store, so it is a file system with an S3 face. Lakekeeper is a Rust Iceberg REST catalog, relevant only because stores now compete on S3 Tables.

## MinIO forks and repackagers

| Project | Facts |
|---|---|
| pgsty/minio ("Silo") | 2.8k stars, AGPL, Go; fork by the Pigsty maintainer from Oct 2025; restores the full console and prebuilt multi-arch binaries, backports CVE fixes, releases every 1–2 months ([repo](https://github.com/pgsty/minio)). HN scepticism about single maintainer. |
| OpenMaxIO | Console-only fork, May 2025, dormant within months |
| Chainguard minio image | Daily rebuilds from source with CVE remediation, not a fork |

## 2025–2026 newcomers

| Project | Facts |
|---|---|
| VaultS3 | Go, AGPL-3.0, 1.6k stars; single binary at 17 MB idle; dashboard; 80+ operations incl. versioning, lock, lifecycle, tagging, CORS, policies, SSE-S3/KMS; Reed-Solomon EC single-node stable, Raft clustering beta; OIDC; external audit Aug 2026; paid add-ons ([repo](https://github.com/Kodiqa-Solutions/VaultS3)) |
| Alarik | Swift/SwiftNIO, Apache-2.0, 533 stars, beta; versioning, conditional requests, policies, lifecycle, webhooks, replication, OIDC console, Reed-Solomon EC ([repo](https://github.com/achtungsoftware/alarik)) |
| HS5 | C++, LGPL-3.0+, 179 stars; single node; LMDB index plus one data file; DuckDB querying; no POST Object, no object lock ([repo](https://github.com/uroni/hs5)) |
| zs3 | Zig, WTFPL, 191 stars; under 1 MB binary; no policies or IAM |

## Proprietary services (pitch reference only)

Cloudflare R2 ($15/TB, zero egress, no versioning or object lock), Tigris ($20/TB, full S3 semantics), Backblaze B2 ($6/TB, versioning always on, object lock), Wasabi (~$7/TB, 90-day minimum), Hetzner (~€6/TB, EU-only). The market treats versioning + object lock + lifecycle + presigned as the baseline "real S3" bar; R2's omissions are the most-cited gotcha.

## What users say they want from a MinIO replacement (2025–2026)

Sources: [HN maintenance-mode thread](https://news.ycombinator.com/item?id=46136023), [Ask HN: open-source alternative to MinIO?](https://news.ycombinator.com/item?id=47977081), [HN pgsty fork thread](https://news.ycombinator.com/item?id=47200342), [HN RustFS Show](https://news.ycombinator.com/item?id=47439450), [MinIO discussion #21326](https://github.com/minio/minio/discussions/21326), [Cloudron forum](https://forum.cloudron.io/topic/13844/minio-removing-the-interface-for-community-edition), [InfoQ](https://www.infoq.com/news/2025/12/minio-s3-api-alternatives/), [productimpossible](https://productimpossible.com/articles/self-hosted-s3-after-minio/), [cloudrumble](https://cloudrumble.net/blog/2025/12/22/minio-to-garage-migration/).

Recurring wants, roughly by frequency:

1. **Trust and governance over features.** "In 2025, 'Open Source' isn't enough. We need Open Governance." Foundation or non-profit backing is a plus; copyright-assignment CLAs and single-vendor open-core are red flags.
2. **A web console with admin functions**: buckets, keys, policies, OIDC, lifecycle, replication. Its removal was the trigger. Garage's missing UI is the most-cited reason to pick something else.
3. **Single static binary, one config file, Docker image and prebuilt binaries.** Losing MinIO binaries hurt as much as the console.
4. **Stability over velocity.** "Shipping updates almost weekly is the opposite of what I want for a mission-critical distributed system."
5. **Licence.** Apache-2.0 preferred by embedders and vendors; self-hosters accept AGPL as rug-pull insurance.
6. **Real S3 features for backup tools**: versioning, object lock, tagging, lifecycle expiration, presigned URLs.
7. **Small footprint, ARM, NAS**: Raspberry Pi, TrueNAS, Synology, 1 GB VPS.
8. **Durability transparency**: fsync semantics, documented on-disk format, recoverability with plain tools.
9. **Erasure coding on a single node with multiple disks**: MinIO's killer homelab feature; Garage's lack of it is the most-cited gap.
10. **Kubernetes**: Helm chart and an operator.
11. **Replication and multi-site.**
12. **Security hygiene**: consistent CVE patching, advisories, no hard-coded secrets.
13. **Migration path**: reading MinIO's on-disk layout, or at least an easy mirror route, and a rollback path.

## Comparison table

Y = supported, P = partial, N = not supported, ? = not documented. As of 2026-09-12.

| Project | Lang / Licence | Stars | Deploy | Redundancy | Versioning | Object lock | Bucket policy | Lifecycle | Notifications | SSE | Replication | Tags | IAM | Admin UI |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| RustFS | Rust / Apache-2.0 | 32.0k | single + distributed | EC | Y | Y | Y | Y | Y | S3/KMS/C | Y | Y | MinIO-style + OIDC | Y |
| Garage | Rust / AGPL | 4.5k | single + geo | replication | N | N | N | P | N | C | N | N | own keys | N |
| SeaweedFS | Go / Apache-2.0 | 34.6k | distributed | replication + EC | Y | Y | Y | Y | N | S3/KMS/C | N | Y | IAM + STS + OIDC | Y |
| Ceph RGW | C++ / LGPL | 17.0k | distributed only | replication or EC | Y | Y | Y | Y | Y | S3/KMS/C | P | Y | IAM + STS | Y |
| Apache Ozone | Java / Apache-2.0 | 1.3k | distributed | replication + EC | N | N | N | N | N | N | N | Y | Ranger | Recon |
| Zenko | JS / Apache-2.0 | 1.9k | single / gateway | backend | Y | stack | Y | stack | stack | Y | stack | ? | Vault | Zenko UI |
| versitygw | Go / Apache-2.0 | 2.8k | gateway | filesystem | P | ? | Y | ? | Y | ? | N | Y | file/LDAP/IAM | Y |
| Swift s3api | Python / Apache-2.0 | 2.8k | distributed | rings | Y | N | N | N | N | Barbican | N | N | Keystone | Horizon |
| pgsty/minio | Go / AGPL | 2.8k | single + distributed | EC | Y | Y | Y | Y | Y | Y | Y | Y | MinIO IAM | Y |
| VaultS3 | Go / AGPL | 1.6k | single (cluster beta) | EC | Y | Y | Y | Y | ? | S3/KMS | beta | Y | IAM + OIDC | Y |
| Alarik | Swift / Apache-2.0 | 533 | single + cluster | EC | Y | ? | Y | Y | webhook | ? | Y | Y | local + OIDC | Y |
| HS5 | C++ / LGPL | 179 | single | none | ? | N | P | ? | ? | ? | N | ? | users | Y |

## Implications for a new open-source S3 store

1. The unoccupied niche is explicit: Apache-2.0 + single static binary + built-in admin console + single-node multi-disk erasure coding + versioning/object lock/tags/lifecycle + honest fsync durability + boring release cadence + non-vendor governance. Garage lacks the first four; RustFS has them but not the trust; SeaweedFS is heavier and now open-core; VaultS3 and Alarik are early and one is AGPL.
2. Publish a real S3 compatibility matrix from day one (Garage's page is the community's gold standard for honesty) and run the Ceph s3-tests suite.
3. Security posture will be judged against RustFS's record: no hard-coded secrets, an advisory for every fix, reproducible builds.
4. Keep the on-disk format documented and recoverable with ordinary tools.
5. Provide a MinIO migration story: reading MinIO's xl.meta layout is a differentiator RustFS only partially delivers.

---

# Positioning: what would make OpenS3 the best option

## The gap in the market, in one paragraph

MinIO's 61,000-star user base is stranded: the code is archived, the binaries and Docker images are gone, the last image is unpatched, and the only continuation (Silo) is a one-person fork of a frozen AGPL codebase with a trademark cloud over it. The replacements each fail a different test. Garage is trusted but has no versioning, object lock, policies, tags, erasure coding or console. RustFS has the features and the licence but a security record (four critical CVEs, ten-plus advisories in three months) and a governance model users distrust. SeaweedFS is mature but architecturally heavy and now open-core. Ceph is a rack-scale system. VaultS3 is AGPL and early; Alarik is Swift and beta. Nobody occupies "complete, correct, trustworthy, simple".

## What users are asking for (from the 2025–2026 threads, in order)

1. Trust and governance over features.
2. An admin console.
3. One static binary, prebuilt images.
4. Stability over release velocity.
5. Apache-2.0.
6. Versioning, object lock, tagging, lifecycle, presigned URLs (the "real S3" bar for backup tools).
7. Small footprint on ARM and NAS hardware.
8. Durability transparency: documented on-disk format, fsync semantics, recoverable with ordinary tools.
9. Erasure coding on a single node with several disks.
10. Kubernetes Helm chart and operator.
11. Replication and multi-site.
12. Security hygiene.
13. A migration path off MinIO.

Items 2, 3, 5, 6, 7, 9 and 10 are table stakes: every credible entrant will have them within a year, and MinIO had all of them in 2024. They are necessary but not a reason to choose us.

## Proposed unique selling points

### USP 1: Fidelity you can verify, not compatibility you are asked to believe

Every open-source store claims "S3 compatible" and every one of them has a hand-maintained compatibility page that lags reality. We make correctness the product:

- **Differential testing against real AWS S3** is the development methodology (the same approach as the okdb project against real kdb+). Every API operation gets a recorded AWS exchange; the test suite asserts byte-level equality of XML bodies, headers, error codes and edge-case semantics (delete markers, null versions, conflicting keys, ETag formats, conditional-write races).
- **A machine-generated conformance matrix** published with every release: the Ceph `s3-tests` suite, the `s3s-e2e` suite and our AWS-recorded suite, each operation marked pass, fail or unsupported, with the failing test names linked. Garage's honest hand-written page is the community's current gold standard; an automatically generated one beats it.
- **The 2024–2026 S3 API, not the 2019 one.** Every alternative targets the S3 that existed when MinIO was designed. AWS has since added conditional writes and deletes, ten checksum algorithms with full-object and composite modes, trailing checksums, GetObjectAttributes, Object Annotations, bucket ABAC, and account-regional namespaces. Modern software built on S3 as a database substrate (Iceberg catalogs, WarpStream-style logs, SlateDB, TurboPuffer) depends on conditional writes and checksums. Being the store where those applications work locally is a concrete reason to pick us.
- **The features MinIO refused to implement**: coexisting keys `a/b` and `a/b/c`, bucket and object ACLs, per-bucket CORS, website hosting, access logging, inventory, PublicAccessBlock, ownership controls, `If-Match` on delete, `AbortIncompleteMultipartUpload`. Each of these is a migration blocker for someone.

Tag line: *If it works on S3, it works on OpenS3. Proven, not claimed.*

### USP 2: The MinIO exit that does not require a copy

Stranded MinIO users have terabytes in MinIO's erasure-coded layout and tooling built on `MINIO_*` variables, the admin API and `mc`. Offer:

- A clean-room reader for MinIO's on-disk format (`xl.meta`, erasure sets, `.minio.sys` bucket metadata, IAM and policy JSON), so an existing data directory is served in place, read-only at first, then migrated bucket by bucket in the background with no downtime and a rollback path (leave the source untouched until the operator confirms). RustFS offers this only behind a non-default feature flag and cannot read encrypted objects.
- Compatibility with the MinIO admin API surface and `mc admin` commands where they are documented, and import of MinIO IAM users, groups, policies and service accounts.
- Import of Garage's key model and SeaweedFS buckets via the S3 API, so people who moved in a hurry in 2025 can consolidate.

This is a legal clean-room effort: read published documentation and the format on disk, do not copy AGPL code.

### USP 3: A trust charter that is enforced by process, not promised in a README

The trigger for the MinIO exodus was "it's a trust issue". Make trust a feature with hard commitments in the repository from day one:

- Apache-2.0 forever, with a Developer Certificate of Origin rather than a copyright-assignment CLA, so no single party can relicense.
- A public "no open-core" rule: every feature the server has is in the free build, and the only commercial offering that could ever exist is support.
- Boring releases: a stable branch with security-fix-only patch releases, a documented support window, and a published security policy with an advisory for every fix and a reproducible, signed build.
- The on-disk format documented in `docs/FORMAT.md` and recoverable with `cat` and a shell script, with a format version number and a compatibility promise.
- An intent to move governance to a foundation or a multi-party steering group once there are contributors outside the founder.

None of this is technology. All of it is what the Hacker News threads said they wanted and what no competitor has written down.

### What we deliberately do not compete on

- Raw throughput records against RustFS or AIStor over RDMA. We must be fast enough (saturate the NICs and disks of ordinary servers) but the benchmark race is a marketing treadmill.
- Exabyte scale-out against Ceph. Distributed mode is on the roadmap for resilience and capacity, not for rack-scale clusters.
- Proprietary AI features (promptObject, AI memory stores).

## What this implies for the earlier open decisions

- **Language.** USP 1 and USP 2 reward development speed, a complete standard library, aws-sdk-based differential tests and the ability to read a format designed by a Go project. That favours Go. If the chosen USP were instead "fastest store on cheap hardware", Rust would be the better fit. The decision remains open.
- **Scope of phase 1.** The conformance harness (recorded AWS exchanges, s3-tests runner, generated matrix) is part of phase 1, not an afterthought, because it is the product.
- **Console.** Must be in the open build and in the first release that we ask anyone to use, because its absence is what started this.
- **Erasure coding.** Single-node multi-disk erasure coding is table stakes for the MinIO audience and cannot slip past phase 2.

## Risks

- Crowded field: RustFS has 32,000 stars and Silo has the MinIO name recognition. Our answer is that neither can make the fidelity or trust claims above, and both have visible weaknesses that the market has already named.
- A new project has no track record; trust is earned over years. The process commitments are checkable from day one, which is the best available substitute.
- Reading MinIO's format is a moving target only if the format changes, and the archived code base means it will not.
- Differential testing against AWS costs money in request fees and needs an AWS account; recorded fixtures keep the recurring cost near zero.
