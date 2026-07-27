# Requirements Document

## Introduction

The AWS Health Event Forwarder is a Go AWS Lambda function that receives AWS Health events delivered by EventBridge and submits Datadog custom metrics. This spec makes the function production-ready by fixing an outage-duration inflation defect and narrowing the function to a single, joinable metric.

EventBridge redelivers the same AWS Health event multiple times across its lifecycle. The previous `aws.health.issue.duration_seconds` metric was a Datadog distribution with no per-outage identity tag, so duplicate deliveries of the same resolved event were indistinguishable from two genuine outages of equal length. A dashboard widget that summed the metric therefore inflated total outage time (an observed `multiple_services` total of 10,325 seconds). No query-only correction is possible: collapsing duplicates requires a stable per-outage identity tag on the metric itself, and the function must remain stateless (no database, no cache).

As prior context, a change that shipped before this spec already added the event ARN as a per-outage identity tag (`event_arn`) on the duration metric, because the ARN is stable across every lifecycle delivery of the same health event, unlike `communicationId`, which changes on each delivery. That tag exists in the current code and is not work this spec introduces.

This spec makes five changes on top of that baseline. First, the metric is renamed to `aws.health.events.duration`. Second, the `event_arn` tag key is renamed to `arn` and its value is emitted in full and untrimmed by removing the trimming helper, so metric series join byte-for-byte against native Datadog AWS Health events, which carry the full ARN. Third, the `received` and `alert_status` metrics are dropped, because the native Datadog AWS Health integration (already enabled in the `aws_is0001_prod` account) supplies event identity, lifecycle, and counts. Fourth, unprocessable payloads are routed to the DLQ instead of being silently skipped. Fifth, the query-time dedup pattern is documented, because deduplication remains a query-time concern and the function stays stateless. The native integration ingests Datadog Events only and cannot compute a duration: AWS Health start and end times are RFC2822 strings, Datadog tag values are strings, and the query layer offers no date parsing or per-row time arithmetic. The Lambda-computed duration is therefore the irreplaceable value this function provides.

The Forwarder emits output only for resolved events. It produces a metric sample only when `detail.statusCode` is `closed`; every other lifecycle delivery (open, upcoming, or any other status) is logged and the invocation ends successfully with no metric sample and no DLQ routing.

Scope is limited to the Lambda source code, its build/packaging configuration, its tests, and its documentation.

## Out of Scope

- Creating or modifying the EventBridge rule, IAM roles/policies, the Lambda function resource, or the Dead Letter Queue resource itself
- Enabling or configuring the native Datadog AWS Health integration (already enabled in `aws_is0001_prod`)
- Datadog API key, site, or client configuration in code (supplied by the Datadog Lambda layer)
- Building Datadog dashboards or monitors (the dedup query pattern is documented, not deployed)

## Glossary

- **Forwarder**: The Go AWS Lambda function in this repository that consumes AWS Health events and submits Datadog metrics.
- **Health_Event**: A single AWS Health event, identified by its `eventArn`, delivered to the Forwarder inside an EventBridge event as the `detail` object.
- **Health_Event_Detail**: The JSON object in the EventBridge `detail` field containing AWS Health fields such as `eventArn`, `service`, `statusCode`, `startTime`, `endTime`, `eventRegion`, `eventTypeCode`.
- **Event_Payload**: The complete, unmodified JSON object the Forwarder receives for one invocation, including envelope fields (`account`, `source`, `detail-type`, `region`) and the `detail` object.
- **Resolved_Event**: A Health_Event whose `detail.statusCode` equals `closed`.
- **Duration_Metric**: The Datadog custom metric named `aws.health.events.duration`, submitted as a distribution, whose value is the outage duration of a Resolved_Event in seconds.
- **Outage_Duration**: `endTime - startTime` of a Resolved_Event, expressed in seconds.
- **Full_ARN**: The `detail.eventArn` value exactly as received, with no truncation or transformation (for example `arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae-af1f-54e6-a526-d0179ed6d68f`).
- **Dead_Letter_Queue**: The pre-existing AWS queue that receives the Event_Payload of invocations the Forwarder cannot process. Referred to as the DLQ.
- **Native_Integration**: The Datadog Amazon Health integration, which ingests AWS Health data as Datadog Events (identity, lifecycle, counts) and emits no metrics.
- **Dedup_Query_Pattern**: The documented Datadog query shape `sum(max:aws.health.events.duration{<scope>} by {arn})`, which collapses duplicate deliveries per outage before summing across outages.
- **Release_Bundle**: The deployment artifact for the Forwarder: a zip archive containing the compiled Linux Lambda binary, produced by GoReleaser.
- **Documentation**: The repository `README.md` file delivered at the end of implementation.

## Requirements

### Requirement 1: Emit Outage Duration As A Single Renamed Distribution Metric

**User Story:** As an observability engineer, I want one clearly named duration metric emitted per resolved AWS Health event, so that I can measure real outage time without inheriting the old inflated metric's history.

#### Acceptance Criteria

1. WHEN the Forwarder processes a Resolved_Event whose `startTime` and `endTime` both parse without error using the AWS Health RFC2822 time layout, THE Forwarder SHALL submit exactly one Datadog distribution sample named `aws.health.events.duration` for that invocation, whose value equals the Outage_Duration computed as `endTime` minus `startTime` expressed as a signed number of seconds at whole-second granularity, with no rounding, clamping, unit conversion, or default substitution applied.
2. THE Forwarder SHALL submit `aws.health.events.duration` as a Datadog distribution metric, and SHALL submit no other Datadog metric type under that metric name.
3. WHEN the Forwarder processes a Health_Event whose `detail.statusCode` is any value other than the exact lowercase string `closed`, including an absent or empty `detail.statusCode`, THE Forwarder SHALL complete the invocation without returning an error, SHALL submit zero Datadog metric samples, and SHALL not route the Event_Payload to the Dead_Letter_Queue.
4. THE Forwarder SHALL use `aws.health.events.duration` as the only Datadog custom metric name in its source and test files, and those files SHALL contain no occurrence of `aws.health.issue.received`, `aws.health.issue.alert_status`, or `aws.health.issue.duration_seconds`.
5. IF a Resolved_Event has a parsed `endTime` earlier than its parsed `startTime`, THEN THE Forwarder SHALL submit the resulting negative Outage_Duration value unchanged, SHALL complete the invocation without returning an error, and SHALL write one log entry that indicates an inverted time range and includes the received `startTime` value, the received `endTime` value, and the `eventArn` value.
6. THE Forwarder SHALL parse `startTime` and `endTime` using the AWS Health RFC2822 time layout `Mon, 2 Jan 2006 15:04:05 GMT`, and SHALL treat both parsed values as UTC instants when computing Outage_Duration.
7. WHEN the Forwarder processes a Resolved_Event whose parsed `endTime` equals its parsed `startTime`, THE Forwarder SHALL submit one `aws.health.events.duration` sample whose value is 0 and SHALL complete the invocation without returning an error.

### Requirement 2: Tag The Metric With A Stable, Join-Compatible Identity

**User Story:** As an observability engineer, I want each duration sample tagged with the untrimmed event ARN, so that duplicates are collapsible at query time and metric series join cleanly with native Datadog AWS Health events.

#### Acceptance Criteria

1. WHEN the Forwarder submits an `aws.health.events.duration` sample, THE Forwarder SHALL attach exactly five tags, each formatted as `key:value`, exactly one tag per key, with the key set being `receiving_account`, `arn`, `aws_service`, `affected_region`, `event_type_code`, and SHALL attach no sixth tag key, in any tag ordering.
2. THE Forwarder SHALL set the `arn` tag value to the Full_ARN, byte-for-byte identical to the received `detail.eventArn`, with no truncation, no splitting, no case change, no whitespace trimming, and no encoding of any character.
3. THE Forwarder SHALL set `receiving_account` to the EventBridge envelope `account` value, `aws_service` to `detail.service`, `affected_region` to `detail.eventRegion`, and `event_type_code` to `detail.eventTypeCode`, each value copied verbatim from the received field with no case change, trimming, or substitution.
4. THE Forwarder SHALL emit the `arn` tag key rather than the previous `event_arn` tag key, SHALL emit no `event_arn`, `event_scope_code`, or `status_code` tag key on any metric sample, and SHALL contain no function that truncates or shortens the `detail.eventArn` value.
5. THE Forwarder SHALL emit each of the five tag strings, counting the key, the colon separator, and the value, at 200 characters or fewer, matching Datadog's 200-character tag limit.
6. THE Forwarder SHALL set the `aws_service` tag value to the `detail.service` value with no case change, no whitespace trimming, and no substitution, so that the value matches the service value carried on Native_Integration events.
7. WHEN the same Health_Event is delivered to the Forwarder two or more times as a Resolved_Event with unchanged `detail` field values, THE Forwarder SHALL emit on every one of those deliveries the same five tag key-value pairs character-for-character and the same metric value with no rounding or numeric difference between deliveries.
8. THE Forwarder SHALL carry the ARN identity under the `arn` tag key only, and SHALL emit no alias tag key carrying the same ARN value, irrespective of any other pipeline component that still references the previous `event_arn` tag key.
9. IF the EventBridge envelope `account` value, `detail.service`, `detail.eventRegion`, or `detail.eventTypeCode` is absent from the Event_Payload or is an empty string, THEN THE Forwarder SHALL emit that tag key with an empty value, SHALL emit the other four tag keys with their received values, and SHALL still submit the `aws.health.events.duration` sample.
10. IF the `arn` tag string formed from the key, the colon separator, and the Full_ARN exceeds 200 characters, THEN THE Forwarder SHALL still emit the Full_ARN untruncated and SHALL write a log entry recording that the tag string exceeded the 200-character limit together with its character count.

### Requirement 3: Route Unprocessable Payloads To The Dead Letter Queue

**User Story:** As an on-call engineer, I want unprocessable health events preserved in the DLQ, so that I can inspect and replay the exact payload that failed instead of losing it silently.

#### Acceptance Criteria

1. IF the Forwarder cannot unmarshal the Event_Payload into a Health_Event_Detail, THEN THE Forwarder SHALL submit zero Datadog metric samples for that invocation and SHALL terminate that invocation with a failure result (a returned error), so that the asynchronous invocation path delivers the complete unmodified Event_Payload to the Dead_Letter_Queue.
2. IF a Resolved_Event has a `startTime` or an `endTime` that is absent, is present as an empty string, or holds a value that is rejected by the AWS Health RFC2822 time layout `Mon, 2 Jan 2006 15:04:05 GMT`, THEN THE Forwarder SHALL submit zero Datadog metric samples for that invocation and SHALL terminate that invocation with a failure result (a returned error), so that the asynchronous invocation path delivers the complete unmodified Event_Payload to the Dead_Letter_Queue.
3. WHEN the Forwarder terminates an invocation with a failure result, THE Forwarder SHALL write exactly one log entry naming the failure cause as exactly one of two values, parse failure or missing/unparseable timestamp, and SHALL include the `detail.eventArn` value in that log entry when `detail.eventArn` is present and non-empty, and SHALL record that the ARN is unavailable when `detail.eventArn` is absent or empty.
4. THE Forwarder SHALL leave the Event_Payload byte-for-byte identical to the bytes it received, adding no field, removing no field, and reformatting no field or value, so that the payload delivered to the Dead_Letter_Queue is replayable without editing.
5. WHEN the Forwarder successfully submits `aws.health.events.duration` for a Resolved_Event, THE Forwarder SHALL complete the invocation with a success result (no returned error) and SHALL NOT terminate the invocation in any way that routes the Event_Payload to the Dead_Letter_Queue.
6. IF a Health_Event whose `detail.statusCode` differs from `closed` has a `startTime` or an `endTime` that is absent, empty, or unparseable with the AWS Health RFC2822 time layout, THEN THE Forwarder SHALL submit zero Datadog metric samples and SHALL complete the invocation with a success result (no returned error), so that the Event_Payload is not routed to the Dead_Letter_Queue.
7. WHEN the same Event_Payload that previously produced a failure result is redelivered or replayed to the Forwarder unchanged, THE Forwarder SHALL produce the same failure result, the same named failure cause, and zero Datadog metric samples on every such invocation.

### Requirement 4: Best-Effort, Stateless Delivery With Query-Time Deduplication

**User Story:** As a platform engineer, I want the Forwarder to stay stateless and best-effort, so that it needs no datastore, no ordering guarantee, and no reconciliation job.

#### Acceptance Criteria

1. THE Forwarder SHALL derive every metric name, metric value, and tag value it emits for an invocation solely from the Event_Payload of that invocation, and SHALL read no persistent store, no cache, and no data written by any prior invocation.
2. THE Forwarder SHALL write no record of a processed Health_Event to any persistent store, cache, or in-memory structure that outlives the invocation, while log entries and Dead_Letter_Queue delivery as defined in Requirement 3 remain permitted side effects.
3. WHEN the Forwarder receives a set of Health_Events in any delivery order, including interleaved and duplicated deliveries of the same `eventArn`, THE Forwarder SHALL emit, for each of those deliveries, the same metric name, the same metric value, and the same tag set that it emits when that delivery is processed in any other order.
4. IF the Forwarder cannot submit an `aws.health.events.duration` sample to Datadog, THEN THE Forwarder SHALL complete the invocation successfully, SHALL make no further submission attempt for that sample within the invocation, SHALL retain no record of the unsubmitted sample, SHALL write a log entry recording the failed submission including the `eventArn` value when that value is available, and SHALL not route the Event_Payload to the Dead_Letter_Queue.
5. THE Documentation SHALL specify the Dedup_Query_Pattern `sum(max:aws.health.events.duration{<scope>} by {arn})`, explaining that the inner `max ... by {arn}` yields one value per outage and the outer `sum` counts each distinct outage once.
6. THE Documentation SHALL state that a table widget using this metric must set rows to `arn` and column aggregation to `max` rather than `sum`.
7. THE Documentation SHALL state that `arn` tag cardinality has an upper bound of one tag value per distinct Health_Event `eventArn`, that the Forwarder applies no truncation, sampling, or other cardinality reduction to that tag, and that this cardinality cost is accepted.
8. THE Documentation SHALL state that metric delivery is best-effort, that a failed submission results in a permanently lost sample with no retry, no reconciliation job, and no Dead_Letter_Queue routing, and that duplicate samples are expected and are resolved at query time using the Dedup_Query_Pattern.
9. WHEN the Forwarder runs in a reused Lambda execution environment, THE Forwarder SHALL emit for a given Event_Payload the same metric name, metric value, and tag set that it emits for that same Event_Payload in a newly initialized execution environment.

### Requirement 5: Document The Native Integration Boundary And Join Compatibility

**User Story:** As a dashboard author, I want the split between native Datadog AWS Health events and this metric documented, so that I query identity from events, duration from the metric, and join the two correctly.

#### Acceptance Criteria

1. THE Documentation SHALL state that Health_Event identity, lifecycle, and counts are obtained from Native_Integration Datadog Events and from no Datadog custom metric, and SHALL state that the `aws.health.issue.received` and `aws.health.issue.alert_status` metrics were removed because the Native_Integration supplies those three properties.
2. THE Documentation SHALL state that the Native_Integration produces Datadog Events only, emits no metric, and cannot produce an Outage_Duration value, and SHALL give both stated reasons: AWS Health `startTime` and `endTime` arrive as RFC2822 strings, and the Datadog query layer provides no per-row date parsing and no per-row time subtraction.
3. THE Documentation SHALL state that the Native_Integration is already enabled in the `aws_is0001_prod` account, that enabling or configuring the Native_Integration is out of scope for the Forwarder, and that a query spanning both sources must filter the AWS account id on each source separately, using the `receiving_account` tag on `aws.health.events.duration` and the AWS account id carried on Native_Integration events.
4. THE Documentation SHALL state that a join between Native_Integration events and `aws.health.events.duration` matches the Full_ARN carried on the Native_Integration event against the `arn` tag value byte-for-byte, and SHALL state that a truncated or otherwise transformed ARN value returns zero matching series rather than a partial match or an error.
5. THE Documentation SHALL state that cross-source filtering by service requires the `aws_service` tag value on `aws.health.events.duration` to be byte-for-byte identical to the service value carried on the corresponding Native_Integration event, and SHALL state that a mismatch returns zero matching series rather than an error.
6. THE Documentation SHALL name exactly one source for each of these four properties: Health_Event identity, Health_Event lifecycle, Health_Event count, and Outage_Duration, assigning the first three to the Native_Integration and Outage_Duration to `aws.health.events.duration`.
7. THE Documentation SHALL state that any comparison of Native_Integration event counts against `aws.health.events.duration` must apply the Dedup_Query_Pattern on the metric side, because duplicate deliveries otherwise yield more metric samples than distinct Native_Integration events for the same outage.

### Requirement 6: Package The Function With GoReleaser

**User Story:** As a release engineer, I want the deployment zip produced by GoReleaser, so that builds are repeatable and the artifact is ready to upload as Lambda code.

#### Acceptance Criteria

1. THE repository SHALL contain a GoReleaser configuration that builds the Forwarder from this Go module and produces the Release_Bundle as a single zip archive.
2. WHEN a GoReleaser build runs, THE build SHALL compile the Forwarder for operating system `linux` and architecture `amd64` (x86_64) as the only build target, and SHALL include the resulting executable in the Release_Bundle zip archive.
3. WHEN a GoReleaser build runs, THE build SHALL place the compiled executable at the top level of the Release_Bundle zip archive under the entry point name `bootstrap` required by the `provided.al2` and `provided.al2023` custom runtimes, with the owner-execute permission bit set, and SHALL include no other executable entry in that archive.
4. WHEN a GoReleaser build runs, THE build SHALL complete with exit status 0 and SHALL write exactly one Release_Bundle zip archive to the repository `dist` directory.
5. IF a GoReleaser build fails to compile the Forwarder or fails GoReleaser configuration validation, THEN THE build SHALL exit with a non-zero status, SHALL write no Release_Bundle zip archive to the repository `dist` directory, and SHALL report an error message identifying the failing compile or validation step.
6. WHEN two GoReleaser builds run from the same repository commit, THE build SHALL produce Release_Bundle archives that contain the same set of entry names, the same entry paths within the archive, and the same entry permission bits.
7. THE Documentation SHALL specify the GoReleaser command used to produce the Release_Bundle, the resulting artifact path within the repository `dist` directory, and the `bootstrap` entry point name inside the archive.

### Requirement 7: Upgrade Dependencies To Latest Versions

**User Story:** As a maintainer, I want the module on the latest dependency versions, so that the function ships with current fixes and a clean build.

#### Acceptance Criteria

1. THE `go.mod` file SHALL declare, for `github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2` and for `github.com/aws/aws-lambda-go`, the highest stable released version available at implementation time, where a stable released version is a published semantic version with no pre-release suffix and no pseudo-version form, selected within the module's existing major version (`v2` for the Datadog contrib module, `v1` for `github.com/aws/aws-lambda-go`).
2. THE `go.mod` file SHALL declare a version no lower than `v2.9.1` for `github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2` and no lower than `v1.54.0` for `github.com/aws/aws-lambda-go`, and SHALL declare a `go` directive value no lower than `1.25.3`.
3. WHEN the dependency upgrade is complete, THE build SHALL compile the module with exit status 0 and zero compile errors, and THE test suite SHALL execute every test present in the repository and pass with exit status 0, zero failures, and zero skipped tests.
4. THE `go.sum` file SHALL contain checksum entries for every module in the build list of the upgraded `go.mod` file and no entries for modules absent from that build list, verified by a module verification command exiting with status 0 and by a module tidy command producing no further changes to `go.mod` or `go.sum`.
5. THE Forwarder source code SHALL contain zero occurrences of a Datadog API key value, a Datadog site value, or a Datadog metrics endpoint value, and zero code paths that read those values from configuration or environment variables, relying instead on the Datadog Lambda layer for Datadog authentication and client configuration.
6. IF declaring the highest stable released version of either module causes the build to exit with a non-zero status or causes one or more tests to fail, THEN THE `go.mod` file SHALL declare the highest stable released version of that module for which the build and the test suite both exit with status 0, and THE Documentation SHALL record the retained version and the reason the higher version was rejected.
7. WHEN the dependency upgrade is complete, THE Forwarder SHALL satisfy every acceptance criterion of Requirements 1, 2, and 3 without changes to the emitted metric name, the emitted tag keys, the emitted tag values, or the emitted metric value for any given Event_Payload.

### Requirement 8: Deliver Documentation

**User Story:** As a new team member, I want a detailed README, so that I can understand what the function emits, why, and how to build and query it without reading the source.

#### Acceptance Criteria

1. THE Documentation SHALL state the purpose of the Forwarder, SHALL name the EventBridge AWS Health event as its only input, SHALL name `aws.health.events.duration` as its only output metric, and SHALL state that the Outage_Duration in seconds is the value the Forwarder supplies that the Native_Integration cannot.
2. THE Documentation SHALL list the metric name `aws.health.events.duration`, its distribution type, its value semantics as Outage_Duration in seconds, and all five tag keys `receiving_account`, `arn`, `aws_service`, `affected_region`, `event_type_code`, each paired with its source field: the EventBridge envelope `account`, the untrimmed `detail.eventArn`, `detail.service`, `detail.eventRegion`, and `detail.eventTypeCode` respectively.
3. THE Documentation SHALL state all three of the following facts about the duplicate-delivery defect: that EventBridge redelivers the same Resolved_Event so that summing the previous metric inflated totals (the observed `multiple_services` total of 10,325 seconds), that no query-only fix is possible because the previous metric carried no per-outage identity tag and the Forwarder must remain stateless with no database and no cache, and that `detail.eventArn` is stable across every delivery of one Health_Event while `communicationId` changes on each delivery.
4. THE Documentation SHALL describe both Dead_Letter_Queue conditions defined in Requirement 3, an Event_Payload that cannot be unmarshalled into a Health_Event_Detail, and a Resolved_Event with a missing or unparseable `startTime` or `endTime`, and SHALL state that the complete unmodified Event_Payload is preserved in the Dead_Letter_Queue and that zero Datadog metric samples are submitted for that invocation.
5. THE Documentation SHALL specify one runnable command for each of the following three tasks: compiling the module, running the test suite, and producing the Release_Bundle with GoReleaser, and SHALL state that the Release_Bundle is written to the repository `dist` directory.
6. THE Documentation SHALL state that Datadog metrics are submitted as distributions because the Datadog Lambda library submits distributions, and that gauge or count submission would require the DogStatsD endpoint on `127.0.0.1:8125` or the Datadog HTTP API.
7. THE Documentation SHALL state that a Health_Event whose `detail.statusCode` differs from `closed` yields a successful invocation with zero metric samples, so that the absence of samples for non-closed events is not read as a failure.
8. THE Documentation SHALL list the removed metric names `aws.health.issue.received`, `aws.health.issue.alert_status`, and `aws.health.issue.duration_seconds`, and SHALL state that the previous `event_arn` tag key is replaced by the `arn` tag key.
9. WHEN implementation is complete, THE Documentation SHALL exist as the repository `README.md` file and SHALL contain a section covering each topic required by criteria 1 through 8 of this requirement.
