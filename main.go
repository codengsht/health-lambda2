// Package main is the entry point for the aws-health-event-forwarder AWS Lambda
// function. It receives AWS Health events (forwarded via EventBridge) and
// emits corresponding metrics to Datadog.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	ddlambda "github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// healthEventTimeFormat is the RFC2822-style format AWS Health uses for time fields.
const healthEventTimeFormat = "Mon, 2 Jan 2006 15:04:05 GMT"

// statusCodeClosed is the AWS Health event statusCode value indicating a
// resolved/closed issue.
const statusCodeClosed = "closed"

// metricNameDuration is the only Datadog custom metric name the Forwarder emits.
const metricNameDuration = "aws.health.events.duration"

// HealthEventDetail maps the AWS Health event detail fields the Forwarder
// reads. Fields the Forwarder does not use are intentionally absent: only these
// seven feed the metric value and its five tags.
type HealthEventDetail struct {
	EventArn      string `json:"eventArn"`
	Service       string `json:"service"`
	EventTypeCode string `json:"eventTypeCode"`
	StatusCode    string `json:"statusCode"`
	StartTime     string `json:"startTime"`
	EndTime       string `json:"endTime"`
	EventRegion   string `json:"eventRegion"`
}

// metricSample describes one intended Datadog submission. It exists so the
// pure decision core can be asserted against without a Datadog client, and so
// the handler has exactly one place where a submission is performed.
type metricSample struct {
	Name  string
	Value float64
	Tags  []string
}

// noticeKind identifies a log intent produced by the pure core.
type noticeKind string

const (
	// noticeNonClosedSkipped records a delivery whose statusCode is not "closed".
	noticeNonClosedSkipped noticeKind = "non_closed_skipped"
	// noticeInvertedRange records an endTime earlier than its startTime.
	noticeInvertedRange noticeKind = "inverted_time_range"
	// noticeTagTooLong records a tag string longer than Datadog's 200-character limit.
	noticeTagTooLong noticeKind = "tag_exceeds_200_chars"
)

// notice is a log intent returned as data rather than written directly, so the
// pure core stays free of I/O and its logging decisions remain assertable.
type notice struct {
	Kind   noticeKind
	Fields map[string]string
}

// evaluation is the result of the pure decision core: what should be submitted
// and what should be logged for one Event_Payload.
type evaluation struct {
	// Sample is nil when nothing should be submitted.
	Sample *metricSample
	// Notices are ordered log intents; may be empty.
	Notices []notice
}

// failureCause names one of the two — and only two — failure causes the
// Forwarder may report.
type failureCause string

const (
	// causeParseFailure marks an Event_Payload detail that cannot be unmarshalled.
	causeParseFailure failureCause = "parse failure"
	// causeBadTimestamp marks a closed event with an absent, empty, or
	// unparseable startTime or endTime.
	causeBadTimestamp failureCause = "missing/unparseable timestamp"
)

// arnUnavailableNote is recorded in place of an ARN when the Event_Payload did
// not yield one.
const arnUnavailableNote = "arn unavailable"

// processingError is the single error type the Forwarder returns. Returning it
// from the handler fails the invocation, which routes the unmodified
// Event_Payload to the DLQ via the Lambda asynchronous invocation path.
type processingError struct {
	Cause    failureCause
	EventArn string // "" when unavailable
	Err      error  // wrapped json/time error, may be nil
}

// Error reports the named cause together with either the event ARN or an
// ARN-unavailable note.
func (e *processingError) Error() string {
	arn := arnUnavailableNote
	if e.EventArn != "" {
		arn = "eventArn=" + e.EventArn
	}
	if e.Err != nil {
		return fmt.Sprintf("cause=%s %s: %v", e.Cause, arn, e.Err)
	}
	return fmt.Sprintf("cause=%s %s", e.Cause, arn)
}

// Unwrap exposes the underlying json/time error, when there is one.
func (e *processingError) Unwrap() error { return e.Err }

// outageDuration returns endRaw minus startRaw as a signed number of seconds.
//
// Both inputs must be non-empty and parseable with healthEventTimeFormat;
// anything else yields a *processingError carrying causeBadTimestamp. An absent
// JSON field and an explicit "" are indistinguishable after unmarshal and are
// treated identically.
//
// The returned value is used exactly as computed: equal instants give 0, an
// inverted range gives a negative value, and no rounding, clamping, math.Abs,
// unit conversion, or default substitution is applied.
func outageDuration(startRaw, endRaw string) (float64, error) {
	start, err := parseHealthEventTime("startTime", startRaw)
	if err != nil {
		return 0, err
	}

	end, err := parseHealthEventTime("endTime", endRaw)
	if err != nil {
		return 0, err
	}

	return end.Sub(start).Seconds(), nil
}

// parseHealthEventTime parses one AWS Health timestamp with the RFC2822 layout,
// reporting an empty or rejected value as a causeBadTimestamp failure.
func parseHealthEventTime(field, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, &processingError{
			Cause: causeBadTimestamp,
			Err:   fmt.Errorf("%s is absent or empty", field),
		}
	}

	parsed, err := time.Parse(healthEventTimeFormat, raw)
	if err != nil {
		return time.Time{}, &processingError{
			Cause: causeBadTimestamp,
			Err:   fmt.Errorf("%s %q is not in format %q: %w", field, raw, healthEventTimeFormat, err),
		}
	}

	return parsed, nil
}

// maxTagLength is Datadog's per-tag limit, counting the key, the colon
// separator, and the value.
const maxTagLength = 200

// Tag keys emitted on every aws.health.events.duration sample.
const (
	tagKeyReceivingAccount = "receiving_account"
	tagKeyARN              = "arn"
	tagKeyAWSService       = "aws_service"
	tagKeyAffectedRegion   = "affected_region"
	tagKeyEventTypeCode    = "event_type_code"
)

// metricTags returns the exactly five tags carried by every
// aws.health.events.duration sample, in a fixed order.
//
// Every value is a raw concatenation of its key and the received field: no
// trimming, no case folding, no escaping, no truncation, and no fallback for an
// empty value. An absent field therefore yields "key:" with an empty value, and
// the sample is still submitted. Because the function is a total, branch-free
// map over its inputs, identical inputs necessarily produce character-identical
// tags on every delivery and in any execution environment.
func metricTags(accountID string, detail HealthEventDetail) []string {
	return []string{
		tagKeyReceivingAccount + ":" + accountID,
		tagKeyARN + ":" + detail.EventArn,
		tagKeyAWSService + ":" + detail.Service,
		tagKeyAffectedRegion + ":" + detail.EventRegion,
		tagKeyEventTypeCode + ":" + detail.EventTypeCode,
	}
}

// oversizedTags returns one noticeTagTooLong per tag string longer than
// maxTagLength, recording the tag key and the tag's character count. Tags are
// only inspected, never modified or truncated: an over-limit value is still
// emitted in full.
func oversizedTags(tags []string) []notice {
	var notices []notice
	for _, tag := range tags {
		if len(tag) <= maxTagLength {
			continue
		}
		key, _, _ := strings.Cut(tag, ":")
		notices = append(notices, notice{
			Kind: noticeTagTooLong,
			Fields: map[string]string{
				"tagKey": key,
				"length": strconv.Itoa(len(tag)),
			},
		})
	}
	return notices
}

// evaluate is the single decision function of the Forwarder: pure,
// deterministic, and total. It reads only its arguments, performs no I/O, and
// touches no package state, so the same inputs always yield the same outputs
// regardless of ordering, repetition, or execution environment.
//
// A statusCode that is not byte-for-byte the lowercase "closed" — including an
// empty value — yields no sample, no error, and a single non-closed notice,
// whatever the timestamps hold. A closed event with an absent, empty, or
// unparseable timestamp yields a zero evaluation and a *processingError naming
// causeBadTimestamp together with the received eventArn. Otherwise the
// evaluation carries exactly one sample whose value is the signed second delta,
// plus an inverted-range notice when that value is negative and one notice per
// over-limit tag.
func evaluate(accountID string, detail HealthEventDetail) (evaluation, error) {
	if detail.StatusCode != statusCodeClosed {
		return evaluation{Notices: []notice{{
			Kind: noticeNonClosedSkipped,
			Fields: map[string]string{
				"statusCode": detail.StatusCode,
				"eventArn":   detail.EventArn,
			},
		}}}, nil
	}

	duration, err := outageDuration(detail.StartTime, detail.EndTime)
	if err != nil {
		// outageDuration already reports causeBadTimestamp but has no ARN to
		// attach; re-report the same cause with the event ARN, carrying the
		// underlying time/validation error forward.
		cause := err
		var perr *processingError
		if errors.As(err, &perr) {
			cause = perr.Err
		}
		return evaluation{}, &processingError{
			Cause:    causeBadTimestamp,
			EventArn: detail.EventArn,
			Err:      cause,
		}
	}

	tags := metricTags(accountID, detail)
	result := evaluation{Sample: &metricSample{
		Name:  metricNameDuration,
		Value: duration,
		Tags:  tags,
	}}

	if duration < 0 {
		result.Notices = append(result.Notices, notice{
			Kind: noticeInvertedRange,
			Fields: map[string]string{
				"startTime": detail.StartTime,
				"endTime":   detail.EndTime,
				"eventArn":  detail.EventArn,
			},
		})
	}

	result.Notices = append(result.Notices, oversizedTags(tags)...)

	return result, nil
}

// submitMetric is the single submission seam of the Forwarder. Production code
// never reassigns it and it holds no per-invocation data; tests replace it with
// a recording fake. ddlambda.Metric submits a Datadog distribution and returns
// nothing, so a submission failure is not observable here (see README).
var submitMetric = ddlambda.Metric

// handleRequest is the impure edge of the Forwarder: it decodes the detail,
// delegates every decision to evaluate, writes the log lines the evaluation
// asks for, and performs at most one metric submission. It contains no
// arithmetic, no status comparison, and no tag construction, and it never
// serializes or forwards the Event_Payload — a returned error fails the
// invocation, which is what routes the unmodified payload to the DLQ.
func handleRequest(ctx context.Context, event events.CloudWatchEvent) error {
	log.Printf("Received AWS Health event: source=%s detail-type=%s region=%s",
		event.Source, event.DetailType, event.Region)

	var detail HealthEventDetail
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		log.Printf("Failing invocation: cause=%s %s: %v", causeParseFailure, arnUnavailableNote, err)
		return &processingError{Cause: causeParseFailure, Err: err}
	}

	result, err := evaluate(event.AccountID, detail)
	if err != nil {
		log.Printf("Failing invocation: cause=%s %s startTime=%q endTime=%q: %v",
			causeOf(err), arnField(detail.EventArn), detail.StartTime, detail.EndTime, err)
		return err
	}

	for _, n := range result.Notices {
		logNotice(n)
	}

	if result.Sample != nil {
		submitMetric(result.Sample.Name, result.Sample.Value, result.Sample.Tags...)
		log.Printf("Submitted metric: name=%s value=%v %s",
			result.Sample.Name, result.Sample.Value, arnField(detail.EventArn))
	}

	return nil
}

// causeOf reports the named failure cause of a processing error.
func causeOf(err error) failureCause {
	var perr *processingError
	if errors.As(err, &perr) {
		return perr.Cause
	}
	return causeParseFailure
}

// arnField renders an event ARN for a log line, or the ARN-unavailable note
// when no ARN was received.
func arnField(eventArn string) string {
	if eventArn == "" {
		return arnUnavailableNote
	}
	return "eventArn=" + eventArn
}

// logNotice writes one log line per notice, with a fixed field order per kind
// so the output does not depend on map iteration order.
func logNotice(n notice) {
	switch n.Kind {
	case noticeNonClosedSkipped:
		log.Printf("Skipped non-closed delivery: statusCode=%q %s",
			n.Fields["statusCode"], arnField(n.Fields["eventArn"]))
	case noticeInvertedRange:
		log.Printf("Inverted time range: startTime=%q endTime=%q %s",
			n.Fields["startTime"], n.Fields["endTime"], arnField(n.Fields["eventArn"]))
	case noticeTagTooLong:
		log.Printf("Tag exceeds %d characters, emitted untruncated: tagKey=%s length=%s",
			maxTagLength, n.Fields["tagKey"], n.Fields["length"])
	default:
		log.Printf("Notice: kind=%s fields=%v", n.Kind, n.Fields)
	}
}

func main() {
	lambda.Start(ddlambda.WrapFunction(handleRequest, nil))
}
