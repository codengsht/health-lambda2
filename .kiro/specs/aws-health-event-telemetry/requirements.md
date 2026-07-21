# Requirements Document

## Introduction

This feature is a Go (Golang) AWS Lambda function that receives AWS Health events delivered from Amazon EventBridge and emits observability telemetry to Datadog. Telemetry is submitted through the Datadog Lambda Go v2 library (DDLambda_Library) and forwarded by the Datadog Lambda Extension. A downstream Datadog dashboard consumes this telemetry to provide organization-wide awareness of public AWS service outage candidates involving the organization's safe-landed services and Regions.

The Lambda's sole responsibility is to process every AWS Health event it receives and emit telemetry derived from that event. It performs no filtering, no state persistence, and no deduplication. All event filtering (Region, safe-landed services, FIS events) is performed by the EventBridge rule before the event reaches the Lambda. Downstream consumers correlate and consolidate on `eventArn` (plus `communicationId` for exact-delivery deduplication).

A critical constraint: Lambda application logs are not available in Datadog for compliance reasons. Therefore the dashboard-required dimensions and the downstream correlation identifiers that must be visible downstream are carried in the metric tags themselves. The metric tags do not carry the complete AWS Health event; fields that are free-text, high-cardinality, timestamp-based, or effectively constant are deliberately not emitted as tags (see "AWS Health event fields deliberately not emitted as metric tags"), and the complete original event is preserved only in the initial raw-event log record, which is not sent to Datadog. All metric tag values are additionally subject to Datadog tag normalization, including case-folding, character replacement, and a 200-character cap, so no tag value is guaranteed to be retained without modification or truncation.

Metric submission is best-effort. Parsing and validation failures remain fail-closed and are returned as non-nil handler errors so that asynchronous retry and the DLQ can handle them, but a locally-detectable metric submission error alone does not fail the invocation: the Health_Telemetry_Lambda logs the condition and continues. As a consequence, if the Datadog Lambda Extension is unhealthy, individual telemetry data points may be silently lost rather than retried through the DLQ; this tradeoff is accepted because the Alert Status Metric represents observed state rather than a guaranteed count and the event volume is low.

The `event_arn` tag carries the substring of the detail `eventArn` that follows the first occurrence of the marker `event/`, so that the tag value stays within the Datadog 200-character normalized tag limit. The complete original `eventArn` value is preserved only in the initial raw-event log record, which is not sent to Datadog.

The AWS Health EventBridge schema referenced by this document is the published schema at [Reference: AWS Health events Amazon EventBridge schema](https://docs.aws.amazon.com/health/latest/ug/aws-health-events-eventbridge-schema.html). Field mandatory/optional designations in this document are derived from that reference. (Content was rephrased for compliance with licensing restrictions.)

## Glossary

- **Health_Telemetry_Lambda**: The Go AWS Lambda function that is the subject of this specification. It receives one AWS Health event per invocation, logs it, validates it, and emits telemetry.
- **EventBridge Envelope**: The outer JSON object delivered to the Lambda by EventBridge, containing the fields `version`, `id`, `detail-type`, `source`, `account`, `time`, `region`, `resources`, and `detail`.
- **AWS Health Detail**: The object contained in the envelope `detail` field, describing the AWS Health event (for example `eventArn`, `service`, `statusCode`, `startTime`, `endTime`).
- **Envelope_Parser**: The component of the Health_Telemetry_Lambda responsible for deserializing the EventBridge Envelope from raw input.
- **Detail_Parser**: The component of the Health_Telemetry_Lambda responsible for deserializing the AWS Health Detail from the envelope `detail` field.
- **Field_Validator**: The component of the Health_Telemetry_Lambda responsible for validating required fields of the EventBridge Envelope and AWS Health Detail.
- **Duration_Calculator**: The component of the Health_Telemetry_Lambda responsible for computing the outage duration in seconds for a Closed Event.
- **Metric_Emitter**: The component of the Health_Telemetry_Lambda responsible for submitting custom metrics to Datadog.
- **Event_Logger**: The component of the Health_Telemetry_Lambda responsible for writing the initial raw-event log record.
- **Result_Logger**: The component of the Health_Telemetry_Lambda responsible for writing the normalized structured success log record.
- **Received Metric**: The custom metric named `aws_health.issue.received`.
- **Alert Status Metric**: The custom metric named `aws_health.issue.alert_status`, submitted through the DDLambda_Library as a distribution and interpreted downstream as observed state (via the per-series `max` or the latest point per series), NOT a true gauge.
- **Duration Metric**: The custom metric named `aws_health.issue.duration_seconds`.
- **Datadog Lambda Extension**: The Datadog agent process running locally within the Lambda execution environment that receives metrics submitted via the DDLambda_Library and forwards telemetry to Datadog.
- **DDLambda_Library**: The Datadog Lambda Go v2 package `github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2` (verified latest v2.9.1 at time of writing; the exact version is to be reconfirmed before implementation).
- **Closed Event**: An AWS Health Detail whose `statusCode` value equals `closed`.
- **Open Event**: An AWS Health Detail whose `statusCode` value equals `open`.
- **Upcoming Event**: An AWS Health Detail whose `statusCode` value equals `upcoming`.
- **Failure Category**: One of the defined categorization labels that cause the Health_Telemetry_Lambda to fail with a non-nil handler error: `envelope_json_parse_failure`, `envelope_validation_failure`, `detail_json_parse_failure`, `detail_validation_failure`, `required_timestamp_failure`, and `unexpected_internal_failure`.
- **Log Annotation**: An internal categorization label recorded in a log record for a best-effort condition that does NOT fail the invocation. The only defined Log Annotation is `metric_submission_failure` (a locally-detectable metric submission error). A Log Annotation is not a Failure Category and never causes the Health_Telemetry_Lambda to return a non-nil error.
- **Event Arn Tag Value**: The value assigned to the `event_arn` tag, derived from the detail `eventArn` as the substring that follows the first occurrence of the literal marker `event/`. WHERE the detail `eventArn` does not contain the marker `event/`, the Event Arn Tag Value is the full detail `eventArn` value. IF the resulting value would exceed the Datadog 200-character normalized tag limit, the value is truncated from the end, retaining the leading characters, to fit within that limit as a last-resort safety measure.
- **Datadog Tag Normalization**: The set of transformations Datadog applies to every metric tag value, including case-folding to lowercase, replacement of characters that are not permitted in tag values, and enforcement of a 200-character cap. All metric tag values emitted by the Health_Telemetry_Lambda are subject to Datadog Tag Normalization; consequently no tag value can be guaranteed to be retained without modification or truncation. The Event Arn Tag Value is the field most likely to approach the 200-character cap, which is the reason the Event Arn Tag Value extraction logic exists; the other tag values (for example `affected_account`, `communication_id`, and `page`) are expected to be well within the cap but remain subject to the same normalization.
- **Receiving Account**: The account ID in the envelope `account` field, which is the account to which the AWS Health event was delivered.
- **Affected Account**: The account ID in the detail `affectedAccount` field, which is the account impacted by the AWS Health event.
- **Detail Timestamp**: A timestamp field within the AWS Health Detail (`startTime`, `endTime`, `lastUpdatedTime`) formatted in the AWS Health day-of-week style, for example `Fri, 27 Jan 2023 06:02:51 GMT`.
- **Safe-Landed Service**: An AWS service that the organization has enabled and tracks (per go/awsserviceenablement). Filtering to Safe-Landed Services is performed by the EventBridge rule and is out of scope for the Health_Telemetry_Lambda.

## Requirements

### Requirement 1: Accept and immediately log every received event

**User Story:** As an operations engineer, I want every incoming AWS Health event to be logged verbatim before any processing, so that I can audit exactly what was delivered even when the payload is malformed.

#### Acceptance Criteria

1. WHEN the Health_Telemetry_Lambda is invoked with an input payload, THE Event_Logger SHALL write a single-line JSON log record before any component reads, parses, or transforms the payload.
2. WHEN the input payload is valid JSON, THE Event_Logger SHALL write a log record of the form `{"record_type":"aws_health_event_received","event":{...original event...}}` where the `event` value reproduces the original parsed JSON payload verbatim, without truncation or modification.
3. IF the input payload is absent, null, empty, or not valid JSON, THEN THE Event_Logger SHALL write a log record of the form `{"record_type":"aws_health_event_received","raw_event":"...escaped input..."}` where the `raw_event` value is the original input rendered as a string with any characters not valid within a JSON string escaped.
4. THE Event_Logger SHALL write the initial log record containing no embedded newline or carriage-return characters, escaping any such characters that appear in the payload.
5. THE Health_Telemetry_Lambda SHALL accept every AWS Health event delivered to it without applying any filtering, sampling, or selection criterion.
6. IF the Event_Logger cannot write the initial raw-event log record, THEN THE Health_Telemetry_Lambda SHALL continue processing the event, including parsing, validation, and metric emission, AND SHALL NOT fail the invocation on account of the initial log-write failure.

### Requirement 2: Parse the EventBridge envelope

**User Story:** As a developer, I want the Lambda to deserialize the EventBridge envelope reliably, so that the embedded AWS Health detail and delivery metadata can be extracted.

#### Acceptance Criteria

1. WHEN the initial log record has been written, THE Envelope_Parser SHALL attempt, exactly once, to deserialize the input payload into an EventBridge Envelope structure, ignoring any top-level fields other than `version`, `id`, `detail-type`, `source`, `account`, `time`, `region`, `resources`, and `detail`.
2. IF the input payload is not valid JSON, or is valid JSON that is not a JSON object, THEN THE Envelope_Parser SHALL stop envelope processing without extracting any envelope field AND SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `envelope_json_parse_failure`.
3. WHEN the input payload has been deserialized as a JSON object, THE Envelope_Parser SHALL extract each of the fields `version`, `id`, `detail-type`, `source`, `account`, `time`, `region`, `resources`, and `detail` that is present, SHALL retain the `detail` field as a raw JSON value without deserializing its contents, and SHALL preserve any absent field as absent for subsequent validation without causing a failure.

### Requirement 3: Validate the EventBridge envelope

**User Story:** As an operations engineer, I want the envelope required fields validated, so that only well-formed deliveries produce telemetry and receiving-account and delivery-region metadata are trustworthy.

#### Acceptance Criteria

1. THE Field_Validator SHALL treat the envelope fields `version`, `id`, `detail-type`, `source`, `account`, `time`, and `region` as required JSON string fields, and the envelope field `detail` as a required JSON object field.
2. IF a required envelope field is absent, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `envelope_validation_failure`.
3. IF a required envelope field is present but is not of its expected JSON type (each of `version`, `id`, `detail-type`, `source`, `account`, `time`, and `region` as a JSON string, and `detail` as a JSON object), THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `envelope_validation_failure`.
4. IF a required envelope string field is present but contains zero characters or only whitespace characters, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `envelope_validation_failure`.
5. IF the envelope `account` field is not a twelve-digit numeric string, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `envelope_validation_failure`.
6. WHEN the envelope `resources` field is absent or empty, THE Field_Validator SHALL treat the EventBridge Envelope as valid with respect to the `resources` field.
7. IF the envelope `time` field is present but cannot be parsed as an RFC 3339 timestamp, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `required_timestamp_failure`.

### Requirement 4: Parse the AWS Health detail

**User Story:** As a developer, I want the embedded AWS Health detail deserialized, so that the event fields can be validated and captured as telemetry tags.

#### Acceptance Criteria

1. WHEN the EventBridge Envelope has passed all required validation, THE Detail_Parser SHALL deserialize the envelope `detail` field into an AWS Health Detail structure.
2. IF the envelope `detail` field cannot be deserialized as a JSON object, THEN THE Detail_Parser SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_json_parse_failure`.
3. WHEN the AWS Health Detail is deserialized, THE Detail_Parser SHALL extract the fields `eventArn`, `service`, `eventTypeCode`, `eventTypeCategory`, `eventScopeCode`, `communicationId`, `startTime`, `endTime`, `lastUpdatedTime`, `statusCode`, `eventRegion`, `eventDescription`, `page`, `totalPages`, `backupEvent`, `affectedAccount`, `actionability`, `personas`, `eventMetadata`, and `affectedEntities` into the AWS Health Detail structure.
4. WHEN a field listed in acceptance criterion 3 is absent from the deserialized JSON object, THE Detail_Parser SHALL capture that field as an unset value without causing a deserialization failure, and SHALL defer presence and type checking to the Field_Validator.
5. IF the Detail_Parser causes the Health_Telemetry_Lambda to fail with Failure Category `detail_json_parse_failure`, THEN THE Health_Telemetry_Lambda SHALL NOT submit any telemetry for the event.

### Requirement 5: Validate required AWS Health detail fields

**User Story:** As an operations engineer, I want the required AWS Health detail fields rigorously validated, so that emitted telemetry is complete and enum-based tags are trustworthy.

#### Acceptance Criteria

1. THE Field_Validator SHALL treat the detail fields `eventArn`, `service`, `eventTypeCode`, `eventTypeCategory`, `eventScopeCode`, `communicationId`, `startTime`, `lastUpdatedTime`, `statusCode`, `eventRegion`, `eventDescription`, `page`, `totalPages`, `backupEvent`, and `affectedAccount` as required.
2. IF a required detail field is absent, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
3. IF a required detail field is present but is not of its expected JSON type, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
4. IF a required detail string field is present but contains zero characters or only whitespace characters, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
5. IF the detail `statusCode` value is not an exact, case-sensitive match to one of `open`, `closed`, or `upcoming`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
6. IF the detail `eventScopeCode` value is not an exact, case-sensitive match to one of `PUBLIC` or `ACCOUNT_SPECIFIC`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
7. IF the detail `eventTypeCategory` value is not an exact, case-sensitive match to one of `issue`, `accountNotification`, `investigation`, or `scheduledChange`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
8. IF the detail `affectedAccount` field is not a string of exactly twelve characters where each character is a decimal digit `0` through `9`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
9. IF the detail `page` value cannot be parsed as an integer greater than or equal to `1`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
10. IF the detail `totalPages` value cannot be parsed as an integer greater than or equal to `1`, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
11. IF the parsed detail `page` value is greater than the parsed detail `totalPages` value, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
12. IF the detail `eventDescription` collection is absent or empty, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
13. IF any element of the detail `eventDescription` collection is not an object carrying a non-empty descriptive text value, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `detail_validation_failure`.
14. IF the detail `startTime` value cannot be parsed as a Detail Timestamp, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `required_timestamp_failure`.
15. IF the detail `lastUpdatedTime` value cannot be parsed as a Detail Timestamp, THEN THE Field_Validator SHALL cause the Health_Telemetry_Lambda to fail with Failure Category `required_timestamp_failure`.

### Requirement 6: Tolerantly decode optional AWS Health detail fields

**User Story:** As an operations engineer, I want optional fields decoded leniently, so that a valid event still produces telemetry even when optional metadata is malformed or missing.

#### Acceptance Criteria

1. THE Field_Validator SHALL treat the detail fields `endTime`, `eventMetadata`, `affectedEntities`, `actionability`, and `personas` as optional.
2. IF an optional detail field is absent, THEN THE Detail_Parser SHALL treat the AWS Health Detail as valid with respect to that field and SHALL omit that field's value from downstream use.
3. IF an optional detail field is present but cannot be decoded into its expected JSON type, THEN THE Detail_Parser SHALL treat the AWS Health Detail as valid with respect to that field and SHALL omit that field's value from downstream use.
4. WHEN an optional detail field is present and can be decoded into its expected JSON type, THE Detail_Parser SHALL retain that field's decoded value for downstream use.
5. IF an optional detail field is absent or cannot be decoded into its expected JSON type, THEN THE Detail_Parser SHALL NOT cause the Health_Telemetry_Lambda to fail and SHALL NOT assign any Failure Category on account of that field.
6. WHEN the detail `endTime` field is absent or cannot be parsed as a Detail Timestamp, THE Duration_Calculator SHALL treat the outage duration as unavailable.

### Requirement 7: Emit the received metric

**User Story:** As a dashboard consumer, I want a metric emitted once per successfully validated event delivery, so that I can count events and updates and break them down by service, Region, and status.

#### Acceptance Criteria

1. WHEN the EventBridge Envelope and AWS Health Detail have passed all required validation, THE Metric_Emitter SHALL submit the Received Metric `aws_health.issue.received` with a numeric value of `1`.
2. THE Metric_Emitter SHALL make exactly one submission attempt for the Received Metric per handler invocation attempt for an event that has passed validation.
3. THE Metric_Emitter SHALL submit the Received Metric through the DDLambda_Library global `Metric` helper.
4. Because the DDLambda_Library `Metric` helper returns no value and is fire-and-forget (the Received Metric sample is buffered and flushed asynchronously by the Datadog Lambda Extension), THE Metric_Emitter SHALL make a single best-effort submission call for the Received Metric and SHALL NOT attempt to observe its delivery success or failure; IF a locally-detectable error occurs while making that submission call (for example a runtime error or panic during the call), THEN THE Metric_Emitter SHALL write a log record carrying the internal `metric_submission_failure` Log Annotation AND THE Health_Telemetry_Lambda SHALL continue without returning a non-nil error on account of that error.
5. IF the EventBridge Envelope and AWS Health Detail have not both passed all required validation, THEN THE Metric_Emitter SHALL NOT submit the Received Metric.

### Requirement 8: Emit the alert status metric

**User Story:** As a dashboard consumer, I want a status metric that reflects whether an event is currently open, interpreted as observed state, so that I can display active outage candidates without series fragmentation.

#### Acceptance Criteria

1. WHEN the EventBridge Envelope and the AWS Health Detail have passed all required validation, THE Metric_Emitter SHALL submit the Alert Status Metric `aws_health.issue.alert_status` through the DDLambda_Library global `Metric` helper as a distribution, making exactly one submission attempt for the Alert Status Metric per handler invocation attempt for an event that has passed validation.
2. WHILE the AWS Health Detail is an Open Event, THE Metric_Emitter SHALL submit the Alert Status Metric with a value of `1`.
3. WHILE the AWS Health Detail is a Closed Event or an Upcoming Event, THE Metric_Emitter SHALL submit the Alert Status Metric with a value of `0`.
4. THE Metric_Emitter SHALL submit the Alert Status Metric with a value of either `0` or `1` and no other value.
5. THE Metric_Emitter SHALL submit the Alert Status Metric through the DDLambda_Library global `Metric` helper, the same channel used for the Received Metric and the Duration Metric.
6. Because the DDLambda_Library `Metric` helper returns no value and is fire-and-forget (the Alert Status Metric sample is buffered and flushed asynchronously by the Datadog Lambda Extension), THE Metric_Emitter SHALL make a single best-effort submission call for the Alert Status Metric and SHALL NOT attempt to observe its delivery success or failure; IF a locally-detectable error occurs while making that submission call (for example a runtime error or panic during the call), THEN THE Metric_Emitter SHALL write a log record carrying the internal `metric_submission_failure` Log Annotation AND THE Health_Telemetry_Lambda SHALL continue without returning a non-nil error on account of that error.
7. THE Alert Status Metric SHALL represent the last status observed for a given outage series (last-write-wins per series), and SHALL NOT be treated as an authoritative guarantee of the current real-world state; the value MAY be stale or briefly out of order if EventBridge delivers lifecycle updates out of sequence, and downstream consumers SHALL treat the value as observed state; because the Alert Status Metric is distribution-backed rather than a true gauge, downstream consumers SHALL query it as the per-series `max` or the latest point per series.

### Requirement 9: Compute and emit the duration metric

**User Story:** As a dashboard consumer, I want the outage duration emitted for resolved events, so that I can report how long each service was impacted over a time window.

#### Acceptance Criteria

1. WHILE the AWS Health Detail is a Closed Event AND the detail `startTime` and `endTime` are both parseable Detail Timestamps AND the parsed `endTime` is greater than or equal to the parsed `startTime`, THE Duration_Calculator SHALL compute the outage duration as the non-negative integer number of whole seconds between the parsed `startTime` and the parsed `endTime`.
2. WHEN the outage duration has been computed, THE Metric_Emitter SHALL make exactly one submission attempt for the Duration Metric `aws_health.issue.duration_seconds` with the computed duration value in seconds per handler invocation attempt.
3. IF the AWS Health Detail is not a Closed Event, THEN THE Metric_Emitter SHALL omit submission of the Duration Metric.
4. IF the detail `endTime` is absent or is not a parseable Detail Timestamp, THEN THE Metric_Emitter SHALL omit submission of the Duration Metric.
5. IF the parsed `endTime` is earlier than the parsed `startTime`, THEN THE Metric_Emitter SHALL omit submission of the Duration Metric.
6. THE Metric_Emitter SHALL submit the Duration Metric through the DDLambda_Library global `Metric` helper.
7. Because the DDLambda_Library `Metric` helper returns no value and is fire-and-forget (the Duration Metric sample is buffered and flushed asynchronously by the Datadog Lambda Extension), THE Metric_Emitter SHALL make a single best-effort submission call for the Duration Metric and SHALL NOT attempt to observe its delivery success or failure; IF a locally-detectable error occurs while making that submission call (for example a runtime error or panic during the call), THEN THE Metric_Emitter SHALL write a log record carrying the internal `metric_submission_failure` Log Annotation AND THE Health_Telemetry_Lambda SHALL continue without returning a non-nil error on account of that error.
8. IF the AWS Health Detail is a Closed Event AND the detail `startTime` is absent or is not a parseable Detail Timestamp, THEN THE Metric_Emitter SHALL omit submission of the Duration Metric.

### Requirement 10: Tag the received and duration metrics

**User Story:** As a dashboard consumer, I want the dashboard-required dimensions and downstream correlation identifiers carried in metric tags, so that I can query and inspect outages without access to Lambda logs.

#### Acceptance Criteria

1. WHEN submitting the Received Metric and the Duration Metric, THE Metric_Emitter SHALL attach the tags `event_arn`, `communication_id`, `affected_account`, `receiving_account`, `aws_service`, `affected_region`, `delivery_region`, `event_type_code`, `event_type_category`, `event_scope_code`, `status_code`, `page`, `total_pages`, `backup_event`, `actionability`, `persona`, and `duration_available`.
2. THE Metric_Emitter SHALL set the tag values from the corresponding fields as follows: `event_arn` to the Event Arn Tag Value derived from detail `eventArn`; `communication_id` from detail `communicationId`; `affected_account` from detail `affectedAccount`; `receiving_account` from envelope `account`; `aws_service` from detail `service`; `affected_region` from detail `eventRegion`; `delivery_region` from envelope `region`; `event_type_code` from detail `eventTypeCode`; `event_type_category` from detail `eventTypeCategory`; `event_scope_code` from detail `eventScopeCode`; `status_code` from detail `statusCode`; `page` from detail `page`; `total_pages` from detail `totalPages`; `backup_event` from detail `backupEvent`.
3. THE Metric_Emitter SHALL set the `event_arn` tag to the Event Arn Tag Value, which is the substring of the detail `eventArn` that follows the first occurrence of the literal marker `event/`; WHERE the detail `eventArn` does not contain the marker `event/`, THE Metric_Emitter SHALL set the `event_arn` tag to the full detail `eventArn` value; IF the resulting `event_arn` tag value would exceed the Datadog 200-character normalized tag limit, THEN THE Metric_Emitter SHALL truncate the value from the end, retaining the leading characters, to fit within that limit as a last-resort safety measure.
4. WHERE the detail `actionability` field is present and decodable, THE Metric_Emitter SHALL set the `actionability` tag to its value.
5. WHERE the detail `actionability` field is absent or not decodable, THE Metric_Emitter SHALL set the `actionability` tag to the literal value `unknown`.
6. WHERE the detail `personas` field is present, decodable, and contains at least one persona identifier, THE Metric_Emitter SHALL set the `persona` tag to the persona identifiers concatenated in ascending lexicographic order and separated by a single comma, such that any two personas collections containing the same identifiers produce an identical `persona` tag value regardless of their original order.
7. WHERE the detail `personas` field is absent, not decodable, or decodable to an empty collection, THE Metric_Emitter SHALL set the `persona` tag to the literal value `unknown`.
8. IF the outage duration was computed for the event by the Duration_Calculator, THEN THE Metric_Emitter SHALL set the `duration_available` tag to the literal value `true`.
9. IF the outage duration was not computed for the event by the Duration_Calculator, THEN THE Metric_Emitter SHALL set the `duration_available` tag to the literal value `false`.
10. THE Metric_Emitter SHALL render the `backup_event` tag as the literal value `true` or `false`, and SHALL render the `page` and `total_pages` tags as their base-10 integer string values.

### Requirement 11: Tag the alert status metric with stable identity dimensions only

**User Story:** As a dashboard consumer, I want the alert status metric tagged only with stable identity dimensions, so that the series is not fragmented across updates and pages, which keeps the per-series `max` or latest-point query meaningful.

#### Acceptance Criteria

1. WHEN submitting the Alert Status Metric, THE Metric_Emitter SHALL attach the tags `affected_account`, `event_arn`, `aws_service`, `affected_region`, `event_type_code`, and `event_scope_code`.
2. WHEN submitting the Alert Status Metric, THE Metric_Emitter SHALL set the tag values from the corresponding fields as follows: `affected_account` from detail `affectedAccount`; `event_arn` to the Event Arn Tag Value derived from detail `eventArn`; `aws_service` from detail `service`; `affected_region` from detail `eventRegion`; `event_type_code` from detail `eventTypeCode`; `event_scope_code` from detail `eventScopeCode`.
3. WHEN submitting the Alert Status Metric, THE Metric_Emitter SHALL exclude the update-varying tags `status_code`, `communication_id`, `page`, `total_pages`, and `backup_event`.
4. WHEN submitting the Alert Status Metric, THE Metric_Emitter SHALL attach no tag other than the six tags `affected_account`, `event_arn`, `aws_service`, `affected_region`, `event_type_code`, and `event_scope_code`.
5. WHEN submitting the Alert Status Metric, THE Metric_Emitter SHALL set the `event_arn` tag to the Event Arn Tag Value derived from the detail `eventArn` per Requirement 10 acceptance criterion 3.

### Requirement 12: Emit a structured success result log

**User Story:** As an operations engineer, I want a second structured log record for every successful invocation, so that a successful run produces an auditable normalized record distinct from the raw-event record.

#### Acceptance Criteria

1. WHEN all required validation has passed and the applicable metric submission attempts (the Received Metric, the Alert Status Metric, and, where applicable, the Duration Metric) have been made, THE Result_Logger SHALL write exactly one single-line JSON log record that satisfies both of the following conditions: its `record_type` value is distinct from the raw-event record's `record_type` value, AND it contains the normalized event information.
2. THE Result_Logger SHALL write the structured success log record only after the Event_Logger has written the initial raw-event log record and after the applicable metric submission attempts have been made.
3. WHEN an invocation completes successfully, THE Health_Telemetry_Lambda SHALL produce at least two single-line JSON log records: the initial raw-event record written by the Event_Logger and the structured success record written by the Result_Logger.
4. THE Result_Logger SHALL include in the structured success record the normalized values used for the tags defined in Requirement 10.
5. IF the Result_Logger cannot write the structured success log record, THEN THE Health_Telemetry_Lambda SHALL fail with Failure Category `unexpected_internal_failure`.

### Requirement 13: Return categorized, non-nil errors on failure

**User Story:** As an operations engineer, I want every parsing or validation failure to return a categorized non-nil Lambda error, so that asynchronous retries and the failure destination or DLQ can handle it, while best-effort telemetry failures do not fail the invocation.

#### Acceptance Criteria

1. IF a parsing, envelope validation, detail validation, required-timestamp, or unexpected internal processing step fails, THEN THE Health_Telemetry_Lambda SHALL return a non-nil error from the Lambda handler.
2. WHEN the Health_Telemetry_Lambda returns an error, THE Health_Telemetry_Lambda SHALL associate the error with exactly one Failure Category from the defined set `envelope_json_parse_failure`, `envelope_validation_failure`, `detail_json_parse_failure`, `detail_validation_failure`, `required_timestamp_failure`, and `unexpected_internal_failure`.
3. WHEN the Health_Telemetry_Lambda returns an error, THE Health_Telemetry_Lambda SHALL include in the returned error an indication of the associated Failure Category that the invoker can observe.
4. IF more than one failure condition applies during an invocation, THEN THE Health_Telemetry_Lambda SHALL associate the error with the Failure Category of the earliest failed processing step.
5. IF a failure occurs that does not correspond to any other defined Failure Category, THEN THE Health_Telemetry_Lambda SHALL associate the error with Failure Category `unexpected_internal_failure`.
6. WHEN all required validation has passed, THE Health_Telemetry_Lambda SHALL return a nil error from the Lambda handler, regardless of whether every applicable metric submission completed successfully.
7. IF a best-effort metric submission failure (Log Annotation `metric_submission_failure`) is the only failure that occurs and all required validation has passed, THEN THE Health_Telemetry_Lambda SHALL return a nil error from the Lambda handler.
8. IF any required validation step fails, THEN THE Health_Telemetry_Lambda SHALL return a non-nil error, and a best-effort metric submission failure SHALL NOT change the associated Failure Category.

### Requirement 14: Perform no filtering, persistence, lifecycle tracking, or deduplication

**User Story:** As a system owner, I want the Lambda to remain stateless and single-purpose, so that all filtering, correlation, and deduplication responsibilities stay with the EventBridge rule and downstream consumers.

#### Acceptance Criteria

1. WHEN the Health_Telemetry_Lambda receives an event that passes validation, THE Health_Telemetry_Lambda SHALL emit telemetry regardless of the event's Region, service, or event-type field values, and SHALL NOT drop, skip, or suppress the event based on those values.
2. THE Health_Telemetry_Lambda SHALL derive the telemetry it emits solely from the content of the current event, without reading or writing any state that persists across invocations.
3. WHEN the Health_Telemetry_Lambda receives an event whose `eventArn`, `communicationId`, and `page` values match those of a previously received event, THE Health_Telemetry_Lambda SHALL emit telemetry for that event without suppressing it as a duplicate.
4. THE Health_Telemetry_Lambda SHALL compute the alert status value and the outage duration solely from the current event's fields, without correlating against previously received events.
5. WHEN the Health_Telemetry_Lambda is invoked repeatedly with identical input payloads, THE Health_Telemetry_Lambda SHALL produce identical telemetry output for each invocation.

### Requirement 15: Preserve downstream correlation identifiers

**User Story:** As a dashboard consumer, I want the identifiers needed for correlation and exact-delivery deduplication carried in telemetry, so that I can consolidate outage lifecycles and dedupe exact deliveries downstream.

#### Acceptance Criteria

1. WHEN submitting the Received Metric, THE Metric_Emitter SHALL set the `event_arn` tag to the Event Arn Tag Value derived from the detail `eventArn` and the `affected_account` tag from the detail `affectedAccount` value, so that downstream consumers can correlate a logical outage lifecycle; the Event Arn Tag Value includes the trailing unique identifier of the detail `eventArn` and is sufficient for lifecycle correlation.
2. WHEN submitting the Received Metric, THE Metric_Emitter SHALL set the `event_arn` tag to the Event Arn Tag Value derived from the detail `eventArn`, the `affected_account` tag from the detail `affectedAccount` value, the `communication_id` tag from the detail `communicationId` value, and the `page` tag from the detail `page` value, so that downstream consumers can identify an exact delivered update using the `event_arn`, `communication_id`, and `page` tags together.
3. WHEN submitting the Received Metric, THE Metric_Emitter SHALL set the `affected_account`, `communication_id`, and `page` tag values from their corresponding detail fields, and these tag values SHALL be subject to Datadog tag normalization, including the same 200-character safety cap defined for the Event Arn Tag Value; these values are expected to be well within that limit (a twelve-digit account ID, a short communication identifier, and a small integer page number), and THE Metric_Emitter SHALL set the `event_arn` tag to the Event Arn Tag Value defined in Requirement 10 acceptance criterion 3 rather than the full detail `eventArn`.

## Out of Scope

The following are explicitly out of scope for the Health_Telemetry_Lambda code covered by this specification:

- EventBridge rule filtering (Region, Safe-Landed Services, FIS event exclusion).
- State persistence, deduplication, or any datastore.
- DLQ resources and their configuration.
- Lambda asynchronous retry and tuning infrastructure configuration.
- Datadog dashboard, widgets, and queries.
- Downstream correlation and consolidation logic.
- Datadog authentication and transport internals.

### AWS Health event fields deliberately not emitted as metric tags

The metric tags carry only the dashboard-required dimensions and the downstream correlation identifiers (see Requirement 10 and Requirement 15). The following AWS Health event fields are parsed and validated where required elsewhere in this specification, but are DELIBERATELY NOT emitted as metric tags. This omission concerns only what becomes a metric TAG; it does not change any parsing or validation requirement. The complete original event remains available only in the initial raw-event log record, which is not sent to Datadog.

- **`eventDescription`**: Free-text payload with no query value that would exceed the Datadog tag length limit; available only in the raw-event log record.
- **`startTime`, `endTime`, `lastUpdatedTime`**: Timestamps are an anti-pattern as tags because they are near-unique and not groupable; `startTime` and `endTime` are already consumed to compute `duration_seconds`, and the metric's own sample timestamp covers when the sample occurred.
- **`eventMetadata`, `affectedEntities`**: Variable, high-cardinality payloads; resource-count metrics were explicitly excluded from v1, and these fields are available only in the raw-event log record.
- **Envelope `resources`**: A list of resource ARNs; a high-cardinality payload with no dashboard axis.
- **Envelope `source` and `detail-type`**: Effectively constant for this pipeline (for example `aws.health` and `AWS Health Event`), so they carry no query value.
- **Envelope `id`**: Unique per delivery; `communication_id` together with `page` already identifies an exact delivery, so `id` would only add cardinality with no dashboard value.
- **Envelope `time`**: The delivery timestamp, reserved as the input for the deferred future metric `aws_health.issue.delivery_delay_seconds`, and not emitted as a tag.

## Notes and Deferred Decisions

- Metrics deliberately excluded from v1: service-specific metrics, Region-specific metrics, separate open/closed metrics, resource-count metrics, and a custom Lambda-failure metric.
- A possible future fourth metric, `aws_health.issue.delivery_delay_seconds`, is deferred.
- The AWS Health EventBridge schema documents personas values with some inconsistency (for example `OPERATIONS` appears in examples while `OPERATIONAL` appears in field descriptions). Because `personas` is optional and decoded tolerantly, the `persona` tag representation does not depend on enum validation.
- Metric tag cardinality decision (Option C): The Received Metric and the Duration Metric deliberately retain the complete tag set, including the high-cardinality identity tags (`event_arn`, `communication_id`) and the lifecycle tags (`page`, `total_pages`). The `event_arn` tag carries the Event Arn Tag Value (the extracted suffix after the first `event/` marker), which still includes the trailing unique identifier and therefore preserves correlation and exact-delivery deduplication semantics. This is an accepted, deliberate tradeoff. The EventBridge rule limits AWS Health events to Safe-Landed Services in only two Regions (us-east-1 and us-west-2), so the expected event and update volume is low and the resulting Datadog custom-metric cardinality and cost are not a concern for this feature. Retaining these tags preserves the downstream correlation and exact-delivery deduplication contract (see Requirement 15). If the event-volume assumption ever changes materially, this decision should be revisited; a candidate mitigation is to move the identity fields off the aggregation metrics to a separate channel such as the Datadog Events API.
- Duration units and display: The Duration Metric `aws_health.issue.duration_seconds` is emitted in whole seconds as the raw unit. Conversion to minutes or hours is a downstream display concern handled in Datadog (via a formula query dividing by 60, or via the metric unit metadata), and the Health_Telemetry_Lambda does not emit minutes. This preserves precision and avoids baking a display choice into the emitted data.
- Best-effort metric submission (Decision A): Locally-detectable metric submission errors (`metric_submission_failure`) are treated as best-effort, and this is the only best-effort Log Annotation. They are recorded as internal Log Annotations, not Failure Categories, and never cause the Health_Telemetry_Lambda to return a non-nil error. Only parsing, envelope validation, detail validation, required-timestamp, and unexpected-internal failures remain fail-closed and are handled by the DLQ. Tradeoff: if the Datadog Lambda Extension is unhealthy, individual telemetry data points may be silently lost rather than retried via the DLQ. This is accepted because the Alert Status Metric represents observed state rather than a guaranteed count and the event volume is low.
- Delivery semantics and metric emission contract (Decision A): The Health_Telemetry_Lambda cannot guarantee exactly-once metric emission. EventBridge uses durable at-least-once delivery, and AWS Lambda asynchronous invocation can produce duplicate invocations even without a function error, so the same AWS Health event delivery may reach the Lambda more than once. There is no atomic transaction across the three metrics (Received, Alert Status, and Duration), so a single invocation can also emit them partially. The contract is therefore one submission attempt per applicable metric per handler invocation attempt, not exactly-once per delivery. Duplicate and partial emissions are expected and are resolved by downstream deduplication on `event_arn` + `communication_id` + `page`. Additionally, all three metrics (the Received Metric, the Alert Status Metric, and the Duration Metric) are submitted via the fire-and-forget DDLambda_Library `Metric` helper: it has no return value and buffers a distribution sample that the Datadog Lambda Extension flushes asynchronously, so the handler cannot observe final delivery success or failure for any of them. Only locally-detectable submission errors (for example a runtime error or panic during the submission call) are recorded; delivery to Datadog is never confirmed from within the handler.
- Alert status semantics (Decision B): The Alert Status Metric represents the last status observed for a given outage series (last-write-wins per series). It may be stale or briefly out of order if EventBridge delivers lifecycle updates out of sequence, and it is not an authoritative guarantee of the current real-world state. Because the Alert Status Metric is now distribution-backed (submitted via `ddlambda.Metric`, not a true gauge), downstream consumers interpret it as observed state by querying the per-series `max` or the latest point per series. This is a semantic clarification only and does not change the `1` = open, `0` = closed or upcoming value rule.
- Decision not to use DogStatsD (Decision A): The earlier plan to submit the Alert Status Metric as a true gauge via DogStatsD to the local Datadog Lambda Extension was dropped. All three metrics now use the DDLambda_Library distribution path for a single, uniform submission mechanism, accepting that the Alert Status Metric is distribution-backed and queried downstream as observed state (per-series `max` or the latest point per series).
- Event Arn Tag Value derivation (Decision C): The `event_arn` tag on the Received, Duration, and Alert Status metrics carries the substring of the detail `eventArn` that follows the first occurrence of the marker `event/` (for example, `arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae-af1f-54e6-a526-d0179ed6d68f` yields `EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae-af1f-54e6-a526-d0179ed6d68f`). This keeps the tag value within the Datadog 200-character normalized tag limit while retaining the trailing unique identifier needed for correlation and deduplication. WHERE the detail `eventArn` does not contain the marker `event/`, the full detail `eventArn` value is used. As a last-resort safety measure, IF the resulting value would still exceed the 200-character limit it is truncated from the end, retaining the leading characters, to fit; truncation can impair exact deduplication and is expected only in pathological cases.
- Event Arn Tag Value caveat (Decision C): Because the ARN's embedded Region prefix (for example `af-south-1`) precedes the `event/` marker, that Region prefix is dropped from the `event_arn` tag. The affected Region and delivery Region are still available as the separate `affected_region` and `delivery_region` tags, but the exact original `eventArn` cannot always be reconstructed from tags alone. The complete original `eventArn` is preserved in the initial raw-event log record (which is not sent to Datadog). This is acceptable for the dashboard use case.
