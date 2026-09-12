# Bucket event notifications

OpenS3 delivers S3 bucket event notifications in the AWS event format to
targets configured by the operator. Clients select events per bucket with
`PutBucketNotificationConfiguration`; they cannot create targets.

## Targets (operator configuration)

Targets are defined with environment variables. Today the only built-in
type is `webhook`; other types (Kafka, NATS, AMQP, MQTT, Redis, Postgres)
plug in by implementing `notify.Target` and calling `Config.AddTarget`.

| Variable | Meaning |
|----------|---------|
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_ENDPOINT` | HTTP(S) URL that receives a `POST` per event (required) |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_AUTH_TOKEN` | sent as `Authorization: Bearer <token>` |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_QUEUE_DIR` | directory for a persistent queue; events are written there before delivery and survive restarts |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_QUEUE_LIMIT` | maximum queued events on disk (0 = unlimited) |

`<NAME>` is any identifier (`PRIMARY`, `AUDIT_HOOK`, …); ARN lookups are
case-insensitive. Each target gets the ARN

    arn:opens3:sqs::<NAME>:webhook

Example:

```sh
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_ENDPOINT=https://hooks.example.com/s3
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_AUTH_TOKEN=secret
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_QUEUE_DIR=/var/lib/opens3/notify/primary
opens3 server --root /data
```

### Delivery semantics

* The request path never waits for delivery. Each target has a bounded
  in-memory queue (10 000 events); when it is full the event is dropped,
  counted in `opens3_notify_events_total{target,result="dropped"}` and
  logged. With `_QUEUE_DIR` set the queue is a directory instead and
  nothing is dropped until `_QUEUE_LIMIT` is reached.
* Webhook delivery is at-least-once: network errors and `408`/`429`/`5xx`
  responses are retried with exponential backoff (500 ms doubling to 30 s,
  up to 6 attempts). A persistent queue additionally retries failed
  entries every 15 s and replays them after a restart. `4xx` responses
  other than 408/429 are treated as permanent and the event is discarded.
* Events for one target are delivered in order (one worker per target).
* Metrics: `opens3_notify_events_total{target,result=sent|failed|dropped}`
  and `opens3_notify_queue_length{target}` on `/opens3/metrics`.
* Shutdown waits up to 10 s for queues to drain.

## Bucket configuration (client side)

Standard S3 `PutBucketNotificationConfiguration`. Queue, Topic and
CloudFunction configurations are all accepted; the ARN must reference a
configured target or the request fails with `InvalidArgument`:

```xml
<NotificationConfiguration>
  <QueueConfiguration>
    <Id>images</Id>
    <Queue>arn:opens3:sqs::PRIMARY:webhook</Queue>
    <Event>s3:ObjectCreated:*</Event>
    <Event>s3:ObjectRemoved:Delete</Event>
    <Filter><S3Key>
      <FilterRule><Name>prefix</Name><Value>photos/</Value></FilterRule>
      <FilterRule><Name>suffix</Name><Value>.jpg</Value></FilterRule>
    </S3Key></Filter>
  </QueueConfiguration>
</NotificationConfiguration>
```

Accepted ARN forms, for migration from other systems:

| ARN | Resolves to |
|-----|-------------|
| `arn:opens3:sqs::NAME:webhook` | target `NAME` (native) |
| `arn:minio:sqs:<region>:NAME:webhook` | target `NAME` (MinIO configurations work unchanged) |
| `arn:aws:sqs:<region>:<account>:NAME` | target `NAME` |
| `arn:aws:sns:<region>:<account>:NAME` | target `NAME` |
| `arn:aws:lambda:<region>:<account>:function:NAME` | target `NAME` |

For `opens3`/`minio` ARNs the trailing type must match the target's type.
`prefix`/`suffix` filter values may contain MinIO-style `*` and `?`
wildcards. Events emitted today: `s3:ObjectCreated:Put|Post|Copy|
CompleteMultipartUpload`, `s3:ObjectRemoved:Delete|DeleteMarkerCreated`,
`s3:ObjectTagging:Put|Delete`, `s3:ObjectAcl:Put`, `s3:ObjectRetention:Put`.

## Event format

Each delivery is one JSON document with a `Records` array holding one
record, following the AWS S3 event message structure (version 2.1) plus
the `contentType` and `userMetadata` object fields MinIO adds:

```json
{
  "Records": [{
    "eventVersion": "2.1",
    "eventSource": "aws:s3",
    "awsRegion": "us-east-1",
    "eventTime": "2026-09-12T10:15:30.123Z",
    "eventName": "ObjectCreated:Put",
    "userIdentity": {"principalId": "<canonical user id>"},
    "requestParameters": {"sourceIPAddress": ""},
    "responseElements": {"x-amz-request-id": "…", "x-amz-id-2": "…"},
    "s3": {
      "s3SchemaVersion": "1.0",
      "configurationId": "images",
      "bucket": {
        "name": "photos",
        "ownerIdentity": {"principalId": "<canonical user id>"},
        "arn": "arn:aws:s3:::photos"
      },
      "object": {
        "key": "photos/my+cat.jpg",
        "size": 1024,
        "eTag": "d41d8cd98f00b204e9800998ecf8427e",
        "versionId": "null",
        "sequencer": "0000000000000042",
        "contentType": "image/jpeg",
        "userMetadata": {"X-Amz-Meta-Camera": "x100"}
      }
    }
  }]
}
```

`key` is URL-encoded as AWS does (space → `+`, `/` kept). `sequencer` is
the hexadecimal version sequence number and orders events for one key.
`eventName` omits the `s3:` prefix, as in AWS. The Go type
`notify.Record` mirrors this schema for reuse by other subsystems.
