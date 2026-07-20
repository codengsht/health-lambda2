# AWS Health Event Lambda

A stateless Go Lambda that turns EventBridge-delivered AWS Health events into
Datadog telemetry:

- one single-line structured JSON log for every invocation; and
- one best-effort distribution sample named `aws.health.issue.received` after a
  valid event has been logged successfully.

Logs are the authoritative outage record. The metric measures notification
activity; its `count` aggregation is not a unique-outage count and is not proof
of end-to-end pipeline health.

The function deliberately does not filter, persist, or deduplicate. Filter
services, regions, event categories, and FIS events in the EventBridge rule.
Correlate lifecycle updates downstream with `event_arn`, and identify exact
redeliveries with `event_arn` plus `communication_id`.

## Processing contract

The handler processes one raw EventBridge payload through a fixed pipeline:

1. Decode the EventBridge envelope inside the application.
2. Decode required AWS Health fields fail-closed.
3. Decode optional fields independently and fail-open.
4. Validate required values and `startTime`.
5. Write one structured log line.
6. Submit one Datadog metric sample on the successful path only.

Receiving metadata comes from the EventBridge envelope. Impact metadata comes
from the Health detail:

| Output | Input |
| --- | --- |
| `eventbridge.receiving_account` | envelope `account` |
| `eventbridge.delivery_region` | envelope `region` |
| `aws_health.affected_account` | detail `affectedAccount` |
| `aws_health.event_region` | detail `eventRegion` |

This distinction matters for AWS Organizations and for AWS Health's backup
Region delivery.

### Required detail fields

The invocation fails when any of these values is absent, null, blank, or has an
incompatible JSON type:

- `eventArn`
- `communicationId`
- `service`
- `eventTypeCode`
- `eventTypeCategory`
- `eventScopeCode`
- `statusCode`
- `startTime` (also must match the AWS Health timestamp format)
- `eventRegion`

`lastUpdatedTime` is decoded as optional because AWS's published
account-specific sample omits it even though the schema table marks it
required. Other optional fields include `endTime`, descriptions, affected
entities/account, pagination and backup metadata, actionability, and personas.
A schema-incompatible optional field is omitted without discarding the event.

### Duration

`aws_health.duration_seconds` is emitted only for `closed` events with valid,
ordered start and end timestamps. Every success record also carries
`duration_calculation_status`:

| Status | Meaning |
| --- | --- |
| `calculated` | Duration is present and final |
| `missing_end_time` | A closed event has no `endTime` |
| `invalid_end_time` | A closed event has an unparseable `endTime` |
| `negative_duration` | `endTime` precedes `startTime` |
| `not_final` | Event is `open` or `upcoming` |
| `unsupported_status` | AWS supplied another status value |

### Metric

The handler calls `ddlambda.Metric` with value `1` and these bounded-cardinality
tags:

- `aws_service`
- `event_type_category`
- `event_scope_code`
- `health_status`
- `event_region`

Per-event identifiers are excluded from metric tags. The call occurs only
after the success log is written. The helper exposes no delivery result, so a
metric panic is contained and cannot turn a logged event into a retried Lambda
invocation.

### Failure behavior

Malformed or invalid events produce an
`aws_health_processing_failure` log with one of three stages:

| Stage | Meaning |
| --- | --- |
| `envelope_parse` | The invocation is not a valid EventBridge JSON object |
| `detail_parse` | `detail` is not an object or a required field has the wrong type |
| `validation` | Required values are missing, blank, or invalid |

The raw payload is never copied into a failure log. The handler then returns a
non-nil error so Lambda can retry the asynchronous invocation and route it to
the configured on-failure destination or Lambda DLQ. An EventBridge target DLQ
only captures failures that happen before Lambda accepts the event.

## Develop

Go 1.25 or newer is required by the pinned Datadog Lambda package.

```sh
make test
make verify
```

The representative AWS payload used by integration tests is in
`internal/health/testdata/public-health-event.json`.

## Build and deploy

Build an ARM64 custom-runtime bootstrap for `provided.al2023`:

```sh
make build
```

For x86-64:

```sh
make build LAMBDA_ARCH=amd64
```

Package `build/bootstrap` at the root of the deployment ZIP. Attach a compatible
Datadog Lambda extension layer, configure `DD_SITE` and a secure API-key source
such as `DD_API_KEY_SECRET_ARN`, and grant the function permission to read that
secret. Configure the EventBridge target as an asynchronous Lambda invocation
and configure a Lambda on-failure destination or Lambda DLQ for handler errors.

Useful references:

- [AWS Health EventBridge schema](https://docs.aws.amazon.com/health/latest/ug/aws-health-events-eventbridge-schema.html)
- [AWS Health backup delivery and deduplication](https://docs.aws.amazon.com/health/latest/ug/about-public-events.html)
- [Datadog Go Lambda instrumentation](https://docs.datadoghq.com/serverless/aws_lambda/instrumentation/go/)
- [Datadog serverless custom metrics](https://docs.datadoghq.com/serverless/aws_lambda/metrics/)

## Layout

```text
cmd/aws-health-event-lambda/  Lambda bootstrap and Datadog wrapper
internal/health/              Decode, validation, records, duration, and handler
internal/health/testdata/     Representative AWS Health payload
```
