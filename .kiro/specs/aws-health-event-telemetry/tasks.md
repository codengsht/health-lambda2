# Implementation Plan: AWS Health Event Telemetry

## Overview

This is a refactor of the existing single-file Go Lambda (`main.go`), not a greenfield build. The sequence narrows the Forwarder to one metric (`aws.health.events.duration`) by first adding the new types and pure core alongside the existing code, then rewriting `handleRequest` over that core, then deleting the legacy tag builders and unused struct fields in the same task that removes their last references. Every task leaves the module compiling and `go test ./...` green, except where a task is itself the verification step.

Language: Go (as used throughout the design). Property-based testing uses `pgregory.net/rapid` v1.3.0 as a test-only dependency.

File layout produced by this plan:

- `main.go` — types, pure core (`outageDuration`, `metricTags`, `oversizedTags`, `evaluate`), edge (`handleRequest`, `main`), `submitMetric` seam
- `generators_test.go` — rapid generators from the design's generator table
- `properties_duration_test.go`, `properties_tags_test.go`, `properties_failure_test.go` — the nine property tests
- `main_test.go` — fake-sink handler examples and edge-case tests
- `hygiene_test.go` — source-scanning tests over `*.go`
- `.goreleaser.yaml` — single `linux/amd64` build producing one `dist/*.zip` with `bootstrap` at the root
- `README.md` — full documentation per the design's section plan

## Tasks

- [x] 1. Test scaffolding and legacy test removal
  - [x] 1.1 Add the property-testing dependency and delete the three obsolete tests
    - Add `pgregory.net/rapid` v1.3.0 as a test-only dependency (`go get pgregory.net/rapid@v1.3.0`)
    - Delete `TestAlertStatusMetricTagsAreStableAcrossLifecycleUpdates`, `TestReceivedMetricTagsIncludeDeliveryContextWithoutUnboundedIDs`, and `TestDurationMetricTagsAreLowCardinality` from `main_test.go`; they assert the removed tag sets (`event_arn:`, `event_scope_code:`, `status_code:`, `event_type_category:`) and call builders that cease to exist
    - Leave `main_test.go` as a compiling `package main` file with no remaining references to the legacy builders
    - Confirm `go build ./...` and `go test ./...` still exit 0
    - _Requirements: 1.4, 7.4_

- [x] 2. Data model changes
  - [x] 2.1 Add the core value types to `main.go`
    - Add `metricSample{Name string; Value float64; Tags []string}`
    - Add `noticeKind` with constants `noticeNonClosedSkipped`, `noticeInvertedRange`, `noticeTagTooLong`, and `notice{Kind noticeKind; Fields map[string]string}`
    - Add `evaluation{Sample *metricSample; Notices []notice}`
    - Add `failureCause` with constants `causeParseFailure` ("parse failure") and `causeBadTimestamp` ("missing/unparseable timestamp"), and `processingError{Cause failureCause; EventArn string; Err error}` with `Error()` (includes the cause and either the ARN or an ARN-unavailable note) and `Unwrap()`
    - Keep the existing code untouched in this task so the module still compiles; `HealthEventDetail` is trimmed in task 4.2 together with the removal of its last readers
    - _Requirements: 3.3, 1.5, 2.10_

- [x] 3. Implement the pure core
  - [x] 3.1 Implement `outageDuration(startRaw, endRaw string) (float64, error)`
    - Return a `*processingError` with cause `causeBadTimestamp` when either input is the empty string or is rejected by `time.Parse(healthEventTimeFormat, ...)`; an absent JSON field and an explicit `""` are treated identically
    - Otherwise return `end.Sub(start).Seconds()` as a signed value with no rounding, clamping, `math.Abs`, unit conversion, or default substitution
    - Reuse the existing `healthEventTimeFormat` constant verbatim
    - _Requirements: 1.1, 1.6, 1.7, 3.2_

  - [x] 3.2 Implement `metricTags` and `oversizedTags`
    - `metricTags(accountID string, detail HealthEventDetail) []string` returns exactly five strings in fixed order: `receiving_account:`+accountID, `arn:`+detail.EventArn (full, untrimmed), `aws_service:`+detail.Service, `affected_region:`+detail.EventRegion, `event_type_code:`+detail.EventTypeCode
    - Raw concatenation only: no `TrimSpace`, no case folding, no escaping, no empty-value fallback, no branches
    - `oversizedTags(tags []string) []notice` returns one `noticeTagTooLong` per tag string longer than 200 characters, recording the tag key and `len(tag)`, and never modifies the tag
    - _Requirements: 2.1, 2.2, 2.3, 2.5, 2.6, 2.8, 2.9, 2.10_

  - [x] 3.3 Implement `evaluate(accountID string, detail HealthEventDetail) (evaluation, error)`
    - Compare `detail.StatusCode` against `statusCodeClosed` with an exact byte comparison (no folding, no trimming); on any other value return `evaluation{Notices: [noticeNonClosedSkipped with statusCode and eventArn]}, nil` and no sample
    - On a closed event call `outageDuration`; on error return a zero `evaluation` and a `*processingError` with cause `causeBadTimestamp` and `EventArn` set from `detail.EventArn`
    - On success build the sample `{Name: "aws.health.events.duration", Value: duration, Tags: metricTags(...)}`, append `noticeInvertedRange` (carrying the received `startTime`, `endTime`, and `eventArn`) when the value is negative, and append the notices from `oversizedTags`
    - Keep the function pure, deterministic, and total: arguments in, values out, no I/O, no package state
    - _Requirements: 1.1, 1.3, 1.5, 1.7, 2.9, 3.2, 3.5, 3.6, 4.1, 4.2, 4.3_

- [x] 4. Rewrite the handler over the pure core
  - [x] 4.1 Add the submission seam and rewrite `handleRequest`
    - Add the single package-level function value `var submitMetric = ddlambda.Metric`, never mutated by production code and holding no per-invocation data
    - Keep the `handleRequest(ctx context.Context, event events.CloudWatchEvent) error` signature and `main`'s `lambda.Start(ddlambda.WrapFunction(handleRequest, nil))` unchanged
    - New body order: log the envelope (`source`, `detail-type`, `region`); `json.Unmarshal` the detail and on failure log one line naming cause `parse failure` plus the ARN-unavailable note and return a `*processingError`; call `evaluate`; on error emit its single log line (cause, ARN or ARN-unavailable note, received timestamps) and return it with no submission; otherwise emit one log line per notice, and when a sample is present call `submitMetric(sample.Name, sample.Value, sample.Tags...)` exactly once and log the metric name, value, and `eventArn`; return `nil`
    - Remove the `aws.health.issue.received` and `aws.health.issue.alert_status` submissions and the alert-status computation; the only remaining metric name is `aws.health.events.duration`
    - No arithmetic, no status comparison, and no tag construction in the handler; no SQS or AWS SDK client and no re-marshalling of the payload on any path
    - _Requirements: 1.2, 1.4, 3.1, 3.3, 3.4, 3.5, 4.2, 4.4, 7.5_

  - [x] 4.2 Delete the legacy builders and trim `HealthEventDetail`
    - Delete `eventArnTagValue`, `receivedMetricTags`, `alertStatusMetricTags`, and `durationMetricTags` outright, with no replacement shortening helper
    - Reduce `HealthEventDetail` to the seven used fields (`EventArn`, `Service`, `EventTypeCode`, `StatusCode`, `StartTime`, `EndTime`, `EventRegion`), removing `EventTypeCategory`, `EventScopeCode`, `CommunicationID`, and `EventDescription`
    - Drop the now-unused `strings` import (and any other import left unused) so the file compiles
    - Confirm `go build ./...` and `go test ./...` exit 0
    - _Requirements: 1.4, 2.4, 2.8_

- [x] 5. Checkpoint - core and handler compile and pass
  - Ensure all tests pass, ask the user if questions arise.

- [x] 6. Property-based tests for the correctness properties
  - [x] 6.1 Build the rapid generators from the design's generator table
    - `genFieldValue` (empty, whitespace-only, mixed case, unicode, 250+ character strings)
    - `genEventArn` (realistic Health ARNs; zero/one/many `/`; values pushing `arn:<value>` below, at, and above 200 characters)
    - `genStatusCode` (`closed`, and arbitrary strings filtered to exclude exactly `closed`, seeded with `""`, `open`, `upcoming`, `Closed`, `CLOSED`, ` closed`)
    - `genInstantPair` (ordered, equal, and inverted pairs; multi-day gaps; year boundaries; truncated to whole seconds and formatted with the layout)
    - `genBadTimestamp` (`""`, ISO-8601, RFC3339, layout with a missing component, non-date noise, valid-shaped but invalid dates)
    - `genMalformedDetail` (truncated JSON, JSON arrays, JSON scalars, wrong types for string fields, empty bytes, non-UTF-8 bytes)
    - Place them in `generators_test.go`; all nine property tests draw from these
    - _Requirements: 1.1, 2.9, 3.2_

  - [x] 6.2 Write property test for closed events yielding one correctly valued sample
    - **Property 1: Closed events with parseable timestamps yield exactly one correctly valued sample**
    - `// Feature: aws-health-event-telemetry, Property 1: Closed events with parseable timestamps yield exactly one correctly valued sample`
    - Set an explicit minimum of 100 iterations; place in `properties_duration_test.go`
    - **Validates: Requirements 1.1, 1.7, 2.9, 3.5**

  - [x] 6.3 Write property test for the five-tag verbatim tag set
    - **Property 2: The tag set is exactly five verbatim key:value pairs carrying the Full_ARN under `arn`**
    - `// Feature: aws-health-event-telemetry, Property 2: The tag set is exactly five verbatim key:value pairs carrying the Full_ARN under arn`
    - Assert no key repeats, no sixth key, byte-for-byte values, and that no tag key is `event_arn`, `event_scope_code`, `status_code`, or `event_type_category`
    - Set an explicit minimum of 100 iterations; place in `properties_tags_test.go`
    - **Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.6, 2.8, 2.9**

  - [x] 6.4 Write property test for the AWS Health time layout round-trip
    - **Property 3: The AWS Health time layout round-trips UTC instants**
    - `// Feature: aws-health-event-telemetry, Property 3: The AWS Health time layout round-trips UTC instants`
    - Set an explicit minimum of 100 iterations; place in `properties_duration_test.go`
    - **Validates: Requirements 1.6**

  - [x] 6.5 Write property test for inverted time ranges
    - **Property 4: An inverted time range emits the negative value and reports it**
    - `// Feature: aws-health-event-telemetry, Property 4: An inverted time range emits the negative value and reports it`
    - Assert the unchanged negative value plus exactly one inverted-range notice carrying the received `startTime`, `endTime`, and `eventArn`
    - Set an explicit minimum of 100 iterations; place in `properties_duration_test.go`
    - **Validates: Requirements 1.5**

  - [x] 6.6 Write property test for bad timestamps on closed events
    - **Property 5: A closed event with an absent, empty, or unparseable timestamp always fails with the timestamp cause**
    - `// Feature: aws-health-event-telemetry, Property 5: A closed event with an absent, empty, or unparseable timestamp always fails with the timestamp cause`
    - Assert no sample, cause `missing/unparseable timestamp`, and the error's ARN field equal to `detail.eventArn`
    - Set an explicit minimum of 100 iterations; place in `properties_failure_test.go`
    - **Validates: Requirements 3.2, 3.3**

  - [x] 6.7 Write property test for unmarshalable detail payloads
    - **Property 6: An unmarshalable detail payload always fails with the parse cause and submits nothing**
    - `// Feature: aws-health-event-telemetry, Property 6: An unmarshalable detail payload always fails with the parse cause and submits nothing`
    - Drive `handleRequest` with `submitMetric` swapped for a recording fake (restored via `t.Cleanup`); assert zero recorded submissions, cause `parse failure`, and an ARN-unavailable record
    - Set an explicit minimum of 100 iterations; place in `properties_failure_test.go`
    - **Validates: Requirements 3.1, 3.3**

  - [x] 6.8 Write property test for non-closed statuses
    - **Property 7: A non-closed status never errors and never emits**
    - `// Feature: aws-health-event-telemetry, Property 7: A non-closed status never errors and never emits`
    - Cover `""`, `open`, `upcoming`, `Closed`, `CLOSED`, and arbitrary unicode, with arbitrary (including absent, empty, unparseable) timestamps
    - Set an explicit minimum of 100 iterations; place in `properties_duration_test.go`
    - **Validates: Requirements 1.3, 3.6**

  - [x] 6.9 Write property test for evaluation purity across order and repetition
    - **Property 8: Evaluation is a pure function of the single payload**
    - `// Feature: aws-health-event-telemetry, Property 8: Evaluation is a pure function of the single payload`
    - Compare outcomes (metric name, exact value, five tag strings character-for-character, notices, named failure cause) for a payload evaluated in arbitrary sequences, orders, duplicates, and repetitions against its isolated first-evaluation outcome
    - Set an explicit minimum of 100 iterations; place in `properties_failure_test.go`
    - **Validates: Requirements 2.7, 3.7, 4.1, 4.3, 4.9, 7.7**

  - [x] 6.10 Write property test for over-limit ARN tags
    - **Property 9: An over-limit ARN tag is emitted untruncated and reported**
    - `// Feature: aws-health-event-telemetry, Property 9: An over-limit ARN tag is emitted untruncated and reported`
    - Assert the `arn` tag value equals the input with no shortening, and that a tag-length notice recording the key and character count appears exactly when `arn:<eventArn>` exceeds 200 characters
    - Set an explicit minimum of 100 iterations; place in `properties_tags_test.go`
    - **Validates: Requirements 2.5, 2.10**

- [x] 7. Checkpoint - property suite green
  - Ensure all tests pass, ask the user if questions arise.

- [x] 8. Example, edge-case, and source-hygiene tests
  - [x] 8.1 Write handler example tests using the fake submission sink
    - Add the recording fake that replaces `submitMetric`, restored with `t.Cleanup`
    - Distribution submission shape: exactly one submission per closed event, with the name `aws.health.events.duration`, the expected value, and the five tags
    - Equal-timestamp example: value exactly `0` and a nil error
    - Best-effort submission example: exactly one submission call, nil error, and a log line carrying the `eventArn`, metric name, and value
    - Place in `main_test.go`
    - _Requirements: 1.2, 1.7, 3.5, 4.4_

  - [x] 8.2 Write single-log-line tests for both failure causes
    - Capture log output and assert exactly one log line for the `parse failure` cause (with the ARN-unavailable record) and exactly one for the `missing/unparseable timestamp` cause (with the `eventArn` and the received timestamps), each returning an error and zero submissions
    - Place in `main_test.go`
    - _Requirements: 3.1, 3.2, 3.3_

  - [x] 8.3 Write source-hygiene tests scanning `*.go`
    - Assert no occurrence of `aws.health.issue.received`, `aws.health.issue.alert_status`, or `aws.health.issue.duration_seconds`
    - Assert `eventArnTagValue` is absent
    - Assert no Datadog API key, site, or metrics-endpoint value and no code path reading them (no `DD_API_KEY`, `DD_SITE`, or endpoint literal, no environment read for them)
    - Assert no SQS or AWS SDK client import and no re-marshalling of the payload (no `json.Marshal`/`Encoder` over the event)
    - Place in `hygiene_test.go`
    - _Requirements: 1.4, 2.4, 3.4, 7.5_

- [x] 9. Package the function with GoReleaser
  - [x] 9.1 Add `.goreleaser.yaml`
    - One `builds` entry: `goos: [linux]`, `goarch: [amd64]`, `binary: bootstrap`, `main: .`, `env: [CGO_ENABLED=0]`, `ldflags: [-s -w]`
    - One `archives` entry: `formats: [zip]`, no wrapping directory, no extra `files` entries, stable `name_template`
    - Default `dist` directory; `changelog` disabled
    - _Requirements: 6.1, 6.2, 6.3_

  - [x] 9.2 Verify the GoReleaser configuration, artifact shape, and build determinism
    - Run `goreleaser check` and confirm exit status 0
    - Run `goreleaser release --snapshot --clean` and confirm exit status 0 and exactly one zip archive in `dist/`
    - List archive entries and assert exactly one `bootstrap` entry at the archive root with the owner-execute bit set and no other executable entry
    - Run the snapshot build a second time and diff the entry listings (names, paths, permission bits) to confirm they match
    - Confirm that a deliberately invalid configuration or compile failure exits non-zero with an identifying message and writes no zip (verify by inspection of the failing run, then restore the working configuration)
    - _Requirements: 6.2, 6.3, 6.4, 6.5, 6.6_

- [x] 10. Verify dependencies and module integrity
  - [x] 10.1 Re-check dependency versions and run the full module checks
    - Run `go list -m -versions` for `github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2` and `github.com/aws/aws-lambda-go`; identify the highest version with no pre-release suffix and no pseudo-version form inside the existing major (`v2` and `v1`)
    - Raise the declared version only if a newer stable version has appeared; otherwise keep `v2.9.1` and `v1.54.0`, and keep the `go` directive at no lower than `1.25.3`
    - If a raised version breaks the build or any test, fall back to the highest stable version that builds and tests clean and record the retained version and the rejection reason for the README
    - Run `go mod tidy` (confirming it leaves `go.mod`/`go.sum` unchanged afterwards and retains the `pgregory.net/rapid` entry), `go mod verify`, `go build ./...`, and `go test ./...`, each exiting 0 with zero failures and zero skips
    - Confirm the emitted metric name, tag keys, tag values, and metric value are unchanged for any given payload after the dependency check
    - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.6, 7.7_

- [x] 11. Write the documentation
  - [x] 11.1 Write the full `README.md` per the design's section plan
    - Purpose: what the Forwarder is, the EventBridge AWS Health event as its only input, `aws.health.events.duration` as its only output, and the Outage_Duration in seconds as the value the native integration cannot supply
    - Metric reference: name, distribution type, value semantics, and the five tag keys each paired with its source field (envelope `account`, untrimmed `detail.eventArn`, `detail.service`, `detail.eventRegion`, `detail.eventTypeCode`)
    - Duplicate-delivery defect: EventBridge redelivery inflating sums (the observed `multiple_services` total of 10,325 seconds), why no query-only fix was possible (no per-outage identity tag, stateless function with no database or cache), and `eventArn` stability versus `communicationId` changing per delivery
    - Query patterns: `sum(max:aws.health.events.duration{<scope>} by {arn})` with the inner-max / outer-sum explanation, the table-widget rule (rows `arn`, column aggregation `max` not `sum`), and the `arn` cardinality bound of one value per distinct `eventArn` with no reduction applied and the cost accepted
    - Delivery semantics: best-effort submission, a lost sample permanently lost with no retry, no reconciliation job, and no DLQ routing; duplicates expected and resolved at query time; the `ddlambda.Metric` no-error limitation
    - Native integration boundary: identity, lifecycle, and counts from native events only; why the native integration cannot compute a duration (RFC2822 strings, no per-row date parsing or subtraction); already enabled in `aws_is0001_prod` and out of scope; per-source account filtering via `receiving_account`; the byte-for-byte Full_ARN join and `aws_service` match with the zero-series consequence of any transformation or mismatch; the one-source-per-property table; count comparisons requiring the dedup pattern
    - Non-closed behaviour: a non-`closed` `statusCode` yields a successful invocation with zero samples, so missing samples are not a failure signal
    - Dead letter queue: both conditions (unmarshalable payload; closed event with a missing or unparseable timestamp), the complete unmodified payload preserved, zero samples for that invocation
    - Build, test, release: `go build ./...`, `go test ./...`, `goreleaser release --snapshot --clean`, the `dist/` artifact path, and the `bootstrap` entry name inside the archive
    - Metric type rationale: distributions because the Datadog Lambda library submits distributions; gauge or count would require DogStatsD on `127.0.0.1:8125` or the Datadog HTTP API
    - Migration notes: removed metric names `aws.health.issue.received`, `aws.health.issue.alert_status`, `aws.health.issue.duration_seconds`, and the `event_arn` to `arn` tag key rename
    - _Requirements: 4.5, 4.6, 4.7, 4.8, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.7, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9_

- [x] 12. Final verification
  - [x] 12.1 Run the full build and test suite and walk the documentation review checklist
    - Run `go build ./...`, `go test ./...`, `go mod verify`, `go mod tidy`, and `goreleaser check`, confirming exit status 0 with zero failures and zero skips and no further changes to `go.mod`/`go.sum`
    - Walk the design's documentation review checklist with one item per documentation criterion (Requirements 4.5–4.8, 5.1–5.7, 6.7, 8.1–8.9) against `README.md`, correcting any gap found
    - _Requirements: 4.5, 4.6, 4.7, 4.8, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.7, 7.3, 7.4, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9_

## Notes

- Tasks marked with `*` are optional and can be skipped for faster MVP
- Task 2.1 adds types only, and task 4.2 performs the struct trim, because the four removed fields are still read by the legacy builders until `handleRequest` is rewritten in 4.1 — this keeps the module compiling at the end of every task
- Documentation-only criteria (Requirements 4.5–4.8, 5.1–5.7, 6.7, 8.1–8.9) are satisfied by task 11.1 and its review checklist in 12.1, not by code
- Requirement 3.4 and DLQ routing are satisfied structurally: no code path serializes or forwards the payload, which task 8.3 asserts by source scan
- Property tests are split across three files so independent properties can be written in parallel without file conflicts

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "9.1"] },
    { "id": 1, "tasks": ["2.1"] },
    { "id": 2, "tasks": ["3.1"] },
    { "id": 3, "tasks": ["3.2"] },
    { "id": 4, "tasks": ["3.3"] },
    { "id": 5, "tasks": ["4.1"] },
    { "id": 6, "tasks": ["4.2"] },
    { "id": 7, "tasks": ["6.1", "9.2"] },
    { "id": 8, "tasks": ["6.2", "6.3", "6.6", "8.1", "8.3"] },
    { "id": 9, "tasks": ["6.4", "6.7", "6.10", "8.2"] },
    { "id": 10, "tasks": ["6.5", "6.9"] },
    { "id": 11, "tasks": ["6.8", "10.1"] },
    { "id": 12, "tasks": ["11.1"] },
    { "id": 13, "tasks": ["12.1"] }
  ]
}
```
