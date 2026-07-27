# AWS Health Event Forwarder

## Purpose

The AWS Health Event Forwarder is a Go AWS Lambda function that turns resolved AWS Health
events into one Datadog custom metric. Its only input is an AWS Health event delivered by
EventBridge, and its only output is the Datadog metric `aws.health.events.duration`.

The value the Forwarder supplies is the outage duration in seconds (`endTime - startTime`),
which the native Datadog Amazon Health integration cannot provide. The native integration
ingests AWS Health data as Datadog Events, and Datadog Events carry no numeric metric value.
Everything else about a health event — identity, lifecycle, counts — comes from the native
integration, not from this function.

The function is stateless: it reads no database and no cache, keeps nothing between
invocations, and derives every metric name, value, and tag solely from the payload of the
invocation it is handling.

## Metric reference

One metric is emitted, and only for events whose `detail.statusCode` is exactly `closed`.

| | |
| --- | --- |
| Name | `aws.health.events.duration` |
| Type | Datadog **distribution** |
| Value | Outage duration in seconds: parsed `detail.endTime` minus parsed `detail.startTime`, signed, whole-second granularity, no rounding, clamping, unit conversion, or default substitution |
| Samples per invocation | Exactly one for a closed event with parseable timestamps; zero otherwise |

Timestamps are parsed with the AWS Health RFC2822 layout `Mon, 2 Jan 2006 15:04:05 GMT`.
The trailing `GMT` is a literal in that layout, so both endpoints are UTC instants and the
difference is zone-independent.

Exactly five tags are attached to every sample — one tag per key, no sixth key:

| Tag key | Source field | Notes |
| --- | --- | --- |
| `receiving_account` | EventBridge envelope `account` | The AWS account that received the event |
| `arn` | `detail.eventArn` | The **full, untrimmed** event ARN, byte-for-byte as received |
| `aws_service` | `detail.service` | Copied verbatim, no case change or trimming |
| `affected_region` | `detail.eventRegion` | The region the health event affects |
| `event_type_code` | `detail.eventTypeCode` | For example `AWS_EC2_OPERATIONAL_ISSUE` |

Every value is copied verbatim: no trimming, case folding, escaping, truncation, or
empty-value fallback. If a source field is absent or empty, the tag is still emitted with an
empty value (`aws_service:`), the other four keys carry their received values, and the sample
is still submitted.

Datadog's per-tag limit is 200 characters, counting the key, the colon, and the value. If
`arn:<eventArn>` exceeds that limit, the Forwarder still emits the ARN in full and untruncated
and writes a log line recording the tag key and its character count. Truncating would break
the byte-for-byte join described below, so the limit is reported rather than enforced.

## Why this exists: the duplicate-delivery defect

EventBridge redelivers the same AWS Health event several times across its lifecycle. The
previous metric, `aws.health.issue.duration_seconds`, was a distribution with no per-outage
identity tag, so two deliveries of one resolved event were indistinguishable from two genuine
outages of equal length. Any dashboard widget that summed the metric inflated total outage
time — the observed `multiple_services` total reached 10,325 seconds.

No query-only fix was possible. Collapsing duplicates requires a stable per-outage identity
tag on the metric itself, and the old metric carried none. The function also has to remain
stateless — no database, no cache — so it cannot remember which events it has already seen and
suppress repeats at submission time.

The identity tag is the event ARN, because `detail.eventArn` is stable across every lifecycle
delivery of one health event. `communicationId`, the other candidate, changes on each delivery
and therefore cannot identify an outage.

## Query patterns

Duplicate samples are expected. Collapse them at query time with the dedup pattern:

```
sum(max:aws.health.events.duration{<scope>} by {arn})
```

The inner `max ... by {arn}` reduces every duplicate delivery of one outage to a single value
(all deliveries of the same resolved event carry the same duration, so the max *is* that
duration). The outer `sum` then adds each distinct outage exactly once. Summing without the
inner `max` is the defect described above.

For a table widget, set **rows** to `arn` and **column aggregation** to `max`, not `sum`. A
`sum` column aggregation re-introduces the inflation inside each row.

Cardinality: the `arn` tag has an upper bound of one tag value per distinct health event
`eventArn`. The Forwarder applies no truncation, no sampling, and no other cardinality
reduction to that tag. This cardinality cost is accepted — it is what makes query-time dedup
and the join against native events possible.

## Delivery semantics

Metric delivery is **best-effort**:

- A sample that Datadog never receives is permanently lost. There is no retry, no
  reconciliation job, and no dead-letter-queue routing for a failed submission.
- Exactly one submission is attempted per closed event, and the invocation returns success
  regardless of the submission's fate.
- No record of an unsubmitted sample is retained anywhere.
- Duplicate samples are expected and are resolved at query time with the dedup pattern above.

The reason is a library constraint. The Forwarder submits through
`ddlambda.Metric(name string, value float64, tags ...string)`, which returns nothing.
Samples are buffered by the wrapper installed by `ddlambda.WrapFunction` and flushed to
Datadog by the Datadog Lambda layer after the handler returns, so a transport failure happens
outside the handler's lifetime and is never reported to it. There is no error value, no
callback, and no status the function can inspect, so there is no submission-error branch in
the code.

What the Forwarder does instead: it logs one line per attempted submission carrying the metric
name, the value, and the `eventArn`. A sample Datadog never ingests is therefore still
reconstructible from CloudWatch Logs, and the Datadog layer writes its own transport errors to
the same log group.

## Native integration boundary

The native Datadog Amazon Health integration and this metric are complementary, and each
property has exactly one source:

| Property | Source |
| --- | --- |
| Health event identity | Native integration Datadog Events |
| Health event lifecycle | Native integration Datadog Events |
| Health event count | Native integration Datadog Events |
| Outage duration | `aws.health.events.duration` |

Identity, lifecycle, and counts come from native integration Datadog Events and from no
Datadog custom metric. The `aws.health.issue.received` and `aws.health.issue.alert_status`
metrics were removed for exactly that reason: the native integration already supplies those
three properties.

The native integration produces Datadog Events only. It emits no metric and cannot produce an
outage duration value, for two reasons: AWS Health `startTime` and `endTime` arrive as RFC2822
strings, and the Datadog query layer provides no per-row date parsing and no per-row time
subtraction. Computing the duration in the Lambda is the only way to get a numeric duration
series.

The native integration is **already enabled** in the `aws_is0001_prod` account. Enabling or
configuring it is out of scope for the Forwarder. A query spanning both sources must filter
the AWS account id on each source separately: use the `receiving_account` tag on
`aws.health.events.duration`, and the AWS account id carried on native integration events.
There is no shared account facet across the two sources.

Joining the two sources matches the full ARN carried on the native integration event against
the `arn` tag value **byte-for-byte**. A truncated or otherwise transformed ARN value returns
zero matching series — not a partial match, and not an error. That silent-empty behaviour is
why the Forwarder emits the ARN untrimmed even when the tag string exceeds 200 characters.

The same applies to service filtering across sources: the `aws_service` tag value on
`aws.health.events.duration` must be byte-for-byte identical to the service value carried on
the corresponding native integration event. A mismatch returns zero matching series rather
than an error.

Comparing native integration event counts against `aws.health.events.duration` requires the
dedup pattern on the metric side. Without it, duplicate deliveries yield more metric samples
than distinct native integration events for the same outage, and the comparison is meaningless.

## Non-closed events

A health event whose `detail.statusCode` is anything other than the exact lowercase string
`closed` — including `open`, `upcoming`, `Closed`, `CLOSED`, an empty value, or an absent
field — yields a **successful invocation with zero metric samples**. The delivery is logged
and the payload is not routed to the dead letter queue.

The comparison is an exact byte comparison: no case folding and no whitespace trimming.

The absence of samples for non-closed events is therefore normal and must not be read as a
failure signal. Most deliveries for a given health event are non-closed; only the resolving
delivery produces a sample.

## Dead letter queue

Two conditions fail the invocation, which routes the payload to the pre-existing dead letter
queue:

1. **Unmarshalable payload** — the EventBridge `detail` cannot be unmarshalled into the
   health event detail structure. Logged as cause `parse failure` with an ARN-unavailable note.
2. **Closed event with a bad timestamp** — `detail.statusCode` is `closed` and `startTime` or
   `endTime` is absent, is an empty string, or is rejected by the layout
   `Mon, 2 Jan 2006 15:04:05 GMT`. Logged as cause `missing/unparseable timestamp` together
   with the `eventArn` and the received timestamps.

In both cases zero Datadog metric samples are submitted for that invocation, and exactly one
log line names the cause.

The **complete, unmodified payload is preserved** in the dead letter queue, so it can be
inspected and replayed without editing. This holds structurally rather than by careful coding:
routing happens by returning an error on the asynchronous invocation path, so the Lambda
service delivers the payload *it* holds — the bytes EventBridge sent. The Forwarder contains
no code path that serializes, edits, or forwards the payload, and no SQS or AWS SDK client.

One consequence of the asynchronous path: retries mean a failing payload is processed more
than once before it is dead-lettered. Because evaluation is a pure function of the payload,
every attempt reaches the same cause and emits zero samples, so retries are harmless.

Note that an inverted time range is *not* a dead-letter condition. If a closed event has a
parsed `endTime` earlier than its parsed `startTime`, the Forwarder submits the negative value
unchanged, logs an inverted-range line carrying the received `startTime`, `endTime`, and
`eventArn`, and completes successfully. Both timestamps parsed, so the payload is not
unprocessable — it is upstream data that contradicts itself, and clamping it would fabricate a
duration and hide the problem.

## Repository layout

```
main.go                        entry point: lambda.Start(ddlambda.WrapFunction(forwarder.Handle, nil))
internal/forwarder/
  doc.go                       package overview and the layering map
  types.go                     HealthEventDetail, metricSample, notice, evaluation
  errors.go                    the two named failure causes and processingError
  duration.go                  outageDuration and the AWS Health RFC2822 layout
  tags.go                      metricTags and oversizedTags
  evaluate.go                  evaluate: the single pure decision function
  handler.go                   Handle: the impure edge, and the submitMetric seam
  *_test.go                    unit, example, property-based, and hygiene tests
.goreleaser.yaml               packaging: one linux/amd64 zip with bootstrap at the root
```

`main.go` holds no behaviour: everything lives in `internal/forwarder`, which keeps the
package importable only from inside this module. `duration.go`, `tags.go`, and `evaluate.go`
are pure — arguments in, values out, no I/O and no package state. `handler.go` is the only
file that performs side effects, and `submitMetric` is the only submission point.

Go requires test files to sit in the same directory as the package they test, so the
`*_test.go` files live in `internal/forwarder` alongside the code. They are not part of the
build: `go build` and GoReleaser ignore `*_test.go` entirely, so the release archive contains
only the `bootstrap` binary, and the test-only `pgregory.net/rapid` dependency is never linked
in.

## Build, test, release

```sh
# Compile the module
go build ./...

# Run the test suite (unit, example, property-based, and source-hygiene tests)
go test ./...

# Produce the Release_Bundle
goreleaser release --snapshot --clean
```

GoReleaser writes the deployment artifact to the repository `dist` directory:

```
dist/aws-health-event-forwarder.zip
```

The archive holds exactly one entry, `bootstrap`, at the archive root with the owner-execute
bit set. `bootstrap` is the entry point name required by the `provided.al2` and
`provided.al2023` custom runtimes; no runtime shim is needed because `lambda.Start` from
`aws-lambda-go` speaks the custom runtime API directly. The build targets `linux/amd64` only,
which is why exactly one zip is produced. `goreleaser check` validates the configuration
without building.

Datadog authentication and client configuration come from the Datadog Lambda layer. The
source contains no Datadog API key, site, or metrics endpoint value, and no code path that
reads them from configuration or the environment.

## Metric type rationale

`aws.health.events.duration` is submitted as a **distribution** because the Datadog Lambda
library submits distributions: `ddlambda.Metric`, `ddlambda.Distribution`, and
`ddlambda.MetricWithTimestamp` all produce distribution submissions.

Submitting a gauge or a count instead would require one of:

- the DogStatsD endpoint on `127.0.0.1:8125`, which means adding the Datadog extension layer
  and a StatsD client, or
- direct calls to the Datadog HTTP API, which means handling a Datadog API key in code — which
  this function is explicitly not allowed to do.

Distribution is also the right type for this data: it preserves every individual sample within
a rollup window, which is exactly what makes the query-time `max ... by {arn}` dedup work. A
gauge would collapse duplicates unpredictably by last-write-wins.

## Migration notes

Metrics removed by this change:

| Removed metric | Replacement |
| --- | --- |
| `aws.health.issue.received` | Native integration Datadog Events (identity, counts) |
| `aws.health.issue.alert_status` | Native integration Datadog Events (lifecycle) |
| `aws.health.issue.duration_seconds` | `aws.health.events.duration` |

Tag change: the previous `event_arn` tag key is replaced by the `arn` tag key, and its value is
now the full, untrimmed ARN. The ARN identity is carried under `arn` only — no alias tag key
carries the same value, even if another pipeline component still references `event_arn`. The
`event_scope_code`, `status_code`, and `event_type_category` tag keys are no longer emitted on
any sample.

Because the metric name changed, the new series starts with no history, which is intentional:
`aws.health.issue.duration_seconds` history is inflated by duplicate deliveries and is not
worth carrying forward. Dashboards and monitors referencing the old metric names or the
`event_arn` tag key must be updated to the new name, the `arn` tag key, and the dedup query
pattern.

## Dependencies

| Module | Version |
| --- | --- |
| `github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2` | `v2.9.1` |
| `github.com/aws/aws-lambda-go` | `v1.54.0` |
| `pgregory.net/rapid` (test only) | `v1.3.0` |
| `go` directive | `1.25.3` |

Both direct runtime dependencies are at the highest stable released version inside their
existing major version (`v2` and `v1` respectively). Pre-release versions such as
`v2.10.0-rc.*` and `v2.11.0-dev` are excluded, since a stable release is a published semantic
version with no pre-release suffix and no pseudo-version form. Nothing was raised at the last
dependency re-check, and no version was rejected on build or test grounds.

`pgregory.net/rapid` is a test-only dependency used for the property-based test suite; it is
not linked into the Lambda binary.
