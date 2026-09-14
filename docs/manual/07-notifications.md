# 7. Notifications

Buckets can announce events (object created, removed, tagged, restored,
lifecycle actions and more) to targets. Targets are configured by the
operator; buckets choose which events go where. `../NOTIFICATIONS.md` has
the full reference and event format.

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
least once. Webhooks are retried with backoff; without a queue directory a
target that is down for long loses events (counted in the
`opens3_notify_events_total` metric); with one, events wait on disk and are
replayed. Only the webhook target exists today; Kafka, NATS, AMQP, MQTT
and Redis are planned.
