# 7. Notifications

Buckets can announce events (object created, removed, tagged, restored,
lifecycle actions and more) to targets. Targets are configured by the
operator; buckets choose which events go where.

## Configure a target

```sh
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_ENDPOINT=https://hooks.example.com/opens3
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_AUTH_TOKEN=secret        # optional Bearer token
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_QUEUE_DIR=/var/lib/opens3/queue/primary   # optional: persist undelivered events
```

The target's ARN is `arn:opens3:sqs::PRIMARY:webhook`; MinIO-style
`arn:minio:sqs::PRIMARY:webhook` is accepted too.

## Subscribe a bucket

```sh
aws s3api put-bucket-notification-configuration --bucket b --notification-configuration '{
  "QueueConfigurations":[{"Id":"all-creates","QueueArn":"arn:opens3:sqs::PRIMARY:webhook",
    "Events":["s3:ObjectCreated:*"],
    "Filter":{"Key":{"FilterRules":[{"Name":"prefix","Value":"uploads/"}]}}}]}'
```

An ARN that names no configured target is rejected.

## Delivery

Events are the AWS `Records` JSON, version 2.1, posted as they happen, at
least once. One event looks like this:

```json
{"Records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-east-1",
  "eventTime":"2026-09-14T09:15:02.417Z","eventName":"ObjectCreated:Put",
  "userIdentity":{"principalId":"alice"},"requestParameters":{"sourceIPAddress":""},
  "responseElements":{"x-amz-request-id":"...","x-amz-id-2":"..."},
  "s3":{"s3SchemaVersion":"1.0","configurationId":"all-creates",
    "bucket":{"name":"b","ownerIdentity":{"principalId":"..."},"arn":"arn:aws:s3:::b"},
    "object":{"key":"uploads/photo.jpg","size":48213,"eTag":"...","versionId":"...","sequencer":"...",
      "contentType":"image/jpeg","userMetadata":{}}}}]}
```

Event names you can subscribe to: `s3:ObjectCreated:*` (`Put`, `Post`,
`Copy`, `CompleteMultipartUpload`), `s3:ObjectRemoved:*` (`Delete`,
`DeleteMarkerCreated`), `s3:ObjectTagging:*`, `s3:ObjectAcl:Put`,
`s3:ObjectRetention:Put`, `s3:ObjectRestore:*`, `s3:LifecycleExpiration:*`,
`s3:LifecycleTransition`. Webhooks are retried with backoff; without a queue directory a
target that is down for long loses events (counted in the
`opens3_notify_events_total` metric); with one, events wait on disk and are
replayed. Webhooks are the only target type in this version.
