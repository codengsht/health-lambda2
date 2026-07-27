package main

// Tests for the AWS Health event forwarder live here.
//
// The three legacy tests that covered the removed received, alert-status, and
// duration-seconds metrics and their tag builders have been deleted. Handler
// example tests run over the fake submission sink defined at the bottom of this
// file; property-based tests live in the properties_*_test.go files.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestOutageDurationReturnsSignedSecondsWithoutAdjustment(t *testing.T) {
	tests := []struct {
		name     string
		startRaw string
		endRaw   string
		want     float64
	}{
		{
			name:     "forward range",
			startRaw: "Mon, 6 Jan 2025 10:00:00 GMT",
			endRaw:   "Mon, 6 Jan 2025 11:30:45 GMT",
			want:     5445,
		},
		{
			name:     "equal instants",
			startRaw: "Mon, 6 Jan 2025 10:00:00 GMT",
			endRaw:   "Mon, 6 Jan 2025 10:00:00 GMT",
			want:     0,
		},
		{
			name:     "inverted range stays negative",
			startRaw: "Mon, 6 Jan 2025 11:30:45 GMT",
			endRaw:   "Mon, 6 Jan 2025 10:00:00 GMT",
			want:     -5445,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := outageDuration(tc.startRaw, tc.endRaw)
			if err != nil {
				t.Fatalf("outageDuration(%q, %q) returned error: %v", tc.startRaw, tc.endRaw, err)
			}
			if got != tc.want {
				t.Errorf("outageDuration(%q, %q) = %v, want %v", tc.startRaw, tc.endRaw, got, tc.want)
			}
		})
	}
}

func TestOutageDurationRejectsEmptyOrUnparseableTimestamps(t *testing.T) {
	valid := "Mon, 6 Jan 2025 10:00:00 GMT"

	tests := []struct {
		name     string
		startRaw string
		endRaw   string
	}{
		{name: "empty startTime", startRaw: "", endRaw: valid},
		{name: "empty endTime", startRaw: valid, endRaw: ""},
		{name: "both empty", startRaw: "", endRaw: ""},
		{name: "iso 8601 startTime", startRaw: "2025-01-06T10:00:00Z", endRaw: valid},
		{name: "noise endTime", startRaw: valid, endRaw: "not a timestamp"},
		{name: "missing component endTime", startRaw: valid, endRaw: "Mon, 6 Jan 2025 GMT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := outageDuration(tc.startRaw, tc.endRaw)
			if err == nil {
				t.Fatalf("outageDuration(%q, %q) = %v, want error", tc.startRaw, tc.endRaw, got)
			}
			if got != 0 {
				t.Errorf("outageDuration(%q, %q) = %v on error, want 0", tc.startRaw, tc.endRaw, got)
			}

			var perr *processingError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *processingError", err)
			}
			if perr.Cause != causeBadTimestamp {
				t.Errorf("cause = %q, want %q", perr.Cause, causeBadTimestamp)
			}
		})
	}
}

func TestMetricTagsAreFiveVerbatimKeyValuePairs(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:      "arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae",
		Service:       " EC2 ",
		EventTypeCode: "AWS_EC2_Operational_Issue",
		EventRegion:   "af-south-1",
	}

	got := metricTags("123456789012", detail)

	want := []string{
		"receiving_account:123456789012",
		"arn:arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae",
		"aws_service: EC2 ",
		"affected_region:af-south-1",
		"event_type_code:AWS_EC2_Operational_Issue",
	}

	if len(got) != len(want) {
		t.Fatalf("metricTags returned %d tags, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMetricTagsEmitEmptyValuesForAbsentFields(t *testing.T) {
	got := metricTags("", HealthEventDetail{})

	want := []string{
		"receiving_account:",
		"arn:",
		"aws_service:",
		"affected_region:",
		"event_type_code:",
	}

	if len(got) != len(want) {
		t.Fatalf("metricTags returned %d tags, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOversizedTagsReportsOnlyTagsPastTheLimit(t *testing.T) {
	atLimit := "arn:" + strings.Repeat("a", maxTagLength-len("arn:"))
	overLimit := atLimit + "b"

	if got := oversizedTags([]string{"aws_service:ec2", atLimit}); len(got) != 0 {
		t.Fatalf("oversizedTags reported %d notices for within-limit tags: %+v", len(got), got)
	}

	got := oversizedTags([]string{"aws_service:ec2", overLimit})
	if len(got) != 1 {
		t.Fatalf("oversizedTags reported %d notices, want 1: %+v", len(got), got)
	}
	if got[0].Kind != noticeTagTooLong {
		t.Errorf("notice kind = %q, want %q", got[0].Kind, noticeTagTooLong)
	}
	if got[0].Fields["tagKey"] != "arn" {
		t.Errorf("notice tagKey = %q, want %q", got[0].Fields["tagKey"], "arn")
	}
	if want := strconv.Itoa(len(overLimit)); got[0].Fields["length"] != want {
		t.Errorf("notice length = %q, want %q", got[0].Fields["length"], want)
	}
	if overLimit != atLimit+"b" {
		t.Error("oversizedTags modified the tag it inspected")
	}
}

func TestEvaluateEmitsOneSampleForClosedEvents(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:      "arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae",
		Service:       "EC2",
		EventTypeCode: "AWS_EC2_OPERATIONAL_ISSUE",
		StatusCode:    statusCodeClosed,
		StartTime:     "Mon, 6 Jan 2025 10:00:00 GMT",
		EndTime:       "Mon, 6 Jan 2025 11:30:45 GMT",
		EventRegion:   "af-south-1",
	}

	got, err := evaluate("123456789012", detail)
	if err != nil {
		t.Fatalf("evaluate returned error: %v", err)
	}
	if got.Sample == nil {
		t.Fatal("evaluate returned no sample for a closed event")
	}
	if got.Sample.Name != "aws.health.events.duration" {
		t.Errorf("sample name = %q, want %q", got.Sample.Name, "aws.health.events.duration")
	}
	if got.Sample.Value != 5445 {
		t.Errorf("sample value = %v, want 5445", got.Sample.Value)
	}
	wantTags := metricTags("123456789012", detail)
	if len(got.Sample.Tags) != len(wantTags) {
		t.Fatalf("sample carried %d tags, want %d: %q", len(got.Sample.Tags), len(wantTags), got.Sample.Tags)
	}
	for i := range wantTags {
		if got.Sample.Tags[i] != wantTags[i] {
			t.Errorf("tag %d = %q, want %q", i, got.Sample.Tags[i], wantTags[i])
		}
	}
	if len(got.Notices) != 0 {
		t.Errorf("evaluate returned %d notices, want 0: %+v", len(got.Notices), got.Notices)
	}
}

func TestEvaluateEmitsZeroForEqualTimestamps(t *testing.T) {
	stamp := "Mon, 6 Jan 2025 10:00:00 GMT"
	got, err := evaluate("123456789012", HealthEventDetail{
		StatusCode: statusCodeClosed,
		StartTime:  stamp,
		EndTime:    stamp,
	})
	if err != nil {
		t.Fatalf("evaluate returned error: %v", err)
	}
	if got.Sample == nil {
		t.Fatal("evaluate returned no sample for a closed event")
	}
	if got.Sample.Value != 0 {
		t.Errorf("sample value = %v, want 0", got.Sample.Value)
	}
}

func TestEvaluateReportsInvertedRangeAndKeepsNegativeValue(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:   "arn:aws:health:af-south-1::event/EC2/CODE/ID",
		StatusCode: statusCodeClosed,
		StartTime:  "Mon, 6 Jan 2025 11:30:45 GMT",
		EndTime:    "Mon, 6 Jan 2025 10:00:00 GMT",
	}

	got, err := evaluate("123456789012", detail)
	if err != nil {
		t.Fatalf("evaluate returned error: %v", err)
	}
	if got.Sample == nil {
		t.Fatal("evaluate returned no sample for a closed event")
	}
	if got.Sample.Value != -5445 {
		t.Errorf("sample value = %v, want -5445", got.Sample.Value)
	}

	inverted := noticesOfKind(got.Notices, noticeInvertedRange)
	if len(inverted) != 1 {
		t.Fatalf("got %d inverted-range notices, want 1: %+v", len(inverted), got.Notices)
	}
	fields := inverted[0].Fields
	if fields["startTime"] != detail.StartTime {
		t.Errorf("notice startTime = %q, want %q", fields["startTime"], detail.StartTime)
	}
	if fields["endTime"] != detail.EndTime {
		t.Errorf("notice endTime = %q, want %q", fields["endTime"], detail.EndTime)
	}
	if fields["eventArn"] != detail.EventArn {
		t.Errorf("notice eventArn = %q, want %q", fields["eventArn"], detail.EventArn)
	}
}

func TestEvaluateAppendsOversizedTagNotices(t *testing.T) {
	longARN := strings.Repeat("a", maxTagLength)
	got, err := evaluate("123456789012", HealthEventDetail{
		EventArn:   longARN,
		StatusCode: statusCodeClosed,
		StartTime:  "Mon, 6 Jan 2025 10:00:00 GMT",
		EndTime:    "Mon, 6 Jan 2025 10:00:00 GMT",
	})
	if err != nil {
		t.Fatalf("evaluate returned error: %v", err)
	}
	if got.Sample == nil {
		t.Fatal("evaluate returned no sample for a closed event")
	}
	if got.Sample.Tags[1] != "arn:"+longARN {
		t.Errorf("arn tag = %q, want %q", got.Sample.Tags[1], "arn:"+longARN)
	}
	if oversized := noticesOfKind(got.Notices, noticeTagTooLong); len(oversized) != 1 {
		t.Fatalf("got %d tag-length notices, want 1: %+v", len(oversized), got.Notices)
	}
}

func TestEvaluateSkipsNonClosedStatusesWithoutError(t *testing.T) {
	for _, status := range []string{"", "open", "upcoming", "Closed", "CLOSED", " closed"} {
		t.Run(strconv.Quote(status), func(t *testing.T) {
			detail := HealthEventDetail{
				EventArn:   "arn:aws:health:af-south-1::event/EC2/CODE/ID",
				StatusCode: status,
				StartTime:  "not a timestamp",
				EndTime:    "",
			}

			got, err := evaluate("123456789012", detail)
			if err != nil {
				t.Fatalf("evaluate returned error for statusCode %q: %v", status, err)
			}
			if got.Sample != nil {
				t.Errorf("evaluate returned a sample for statusCode %q: %+v", status, got.Sample)
			}
			skipped := noticesOfKind(got.Notices, noticeNonClosedSkipped)
			if len(skipped) != 1 {
				t.Fatalf("got %d non-closed notices, want 1: %+v", len(skipped), got.Notices)
			}
			if skipped[0].Fields["statusCode"] != status {
				t.Errorf("notice statusCode = %q, want %q", skipped[0].Fields["statusCode"], status)
			}
			if skipped[0].Fields["eventArn"] != detail.EventArn {
				t.Errorf("notice eventArn = %q, want %q", skipped[0].Fields["eventArn"], detail.EventArn)
			}
		})
	}
}

func TestEvaluateFailsClosedEventsWithBadTimestamps(t *testing.T) {
	valid := "Mon, 6 Jan 2025 10:00:00 GMT"
	arn := "arn:aws:health:af-south-1::event/EC2/CODE/ID"

	tests := []struct {
		name     string
		startRaw string
		endRaw   string
	}{
		{name: "empty startTime", startRaw: "", endRaw: valid},
		{name: "empty endTime", startRaw: valid, endRaw: ""},
		{name: "unparseable startTime", startRaw: "2025-01-06T10:00:00Z", endRaw: valid},
		{name: "unparseable endTime", startRaw: valid, endRaw: "noise"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evaluate("123456789012", HealthEventDetail{
				EventArn:   arn,
				StatusCode: statusCodeClosed,
				StartTime:  tc.startRaw,
				EndTime:    tc.endRaw,
			})
			if err == nil {
				t.Fatalf("evaluate returned no error, got %+v", got)
			}
			if got.Sample != nil || len(got.Notices) != 0 {
				t.Errorf("evaluate returned a non-zero evaluation on error: %+v", got)
			}

			var perr *processingError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *processingError", err)
			}
			if perr.Cause != causeBadTimestamp {
				t.Errorf("cause = %q, want %q", perr.Cause, causeBadTimestamp)
			}
			if perr.EventArn != arn {
				t.Errorf("EventArn = %q, want %q", perr.EventArn, arn)
			}
		})
	}
}

// noticesOfKind filters an evaluation's notices down to one kind.
func noticesOfKind(notices []notice, kind noticeKind) []notice {
	var matched []notice
	for _, n := range notices {
		if n.Kind == kind {
			matched = append(matched, n)
		}
	}
	return matched
}

// --- Handler test scaffolding: the fake submission sink and log capture ------
//
// These helpers are shared by every test that drives handleRequest, including
// the rapid property tests in properties_failure_test.go, so they accept the
// small interface both *testing.T and *rapid.T satisfy rather than *testing.T.

// cleanupTB is the slice of *testing.T and *rapid.T the handler scaffolding
// needs: failure reporting plus cleanup registration scoped to the current test
// (for *testing.T) or the current property iteration (for *rapid.T).
type cleanupTB interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// recordedSubmission is one captured call to the submitMetric seam.
type recordedSubmission struct {
	Name  string
	Value float64
	Tags  []string
}

// recordSubmissions replaces the package-level submitMetric seam with a fake
// that appends every call to a slice, and restores the previous value through
// t.Cleanup. The returned pointer is read after driving handleRequest, so tests
// can assert the exact number of submissions and their contents without a
// Datadog endpoint. Tags are copied, so a later reuse of the caller's slice
// cannot alter what was recorded.
func recordSubmissions(t cleanupTB) *[]recordedSubmission {
	t.Helper()

	var recorded []recordedSubmission
	previous := submitMetric
	submitMetric = func(name string, value float64, tags ...string) {
		recorded = append(recorded, recordedSubmission{
			Name:  name,
			Value: value,
			Tags:  append([]string(nil), tags...),
		})
	}
	t.Cleanup(func() { submitMetric = previous })

	return &recorded
}

// captureLogOutput redirects the standard logger into a buffer for the duration
// of the test, dropping timestamps so log lines can be compared verbatim, and
// restores the previous writer and flags through t.Cleanup.
func captureLogOutput(t cleanupTB) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	return &buf
}

// healthEvent builds an EventBridge delivery carrying detail as its JSON detail
// object, so handler tests exercise the same unmarshal path production does.
func healthEvent(t cleanupTB, accountID string, detail HealthEventDetail) events.CloudWatchEvent {
	t.Helper()

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshalling detail failed: %v", err)
	}

	return events.CloudWatchEvent{
		Version:    "0",
		ID:         "0f5c3f0f-1b0a-4e63-9a1a-0c0a1c2f3d4e",
		DetailType: "AWS Health Event",
		Source:     "aws.health",
		AccountID:  accountID,
		Region:     "us-east-1",
		Detail:     raw,
	}
}

// --- Handler example tests ---------------------------------------------------

func TestHandleRequestSubmitsOneSamplePerClosedEvent(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:      "arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae",
		Service:       "EC2",
		EventTypeCode: "AWS_EC2_OPERATIONAL_ISSUE",
		StatusCode:    statusCodeClosed,
		StartTime:     "Mon, 6 Jan 2025 10:00:00 GMT",
		EndTime:       "Mon, 6 Jan 2025 11:30:45 GMT",
		EventRegion:   "af-south-1",
	}

	submissions := recordSubmissions(t)
	captureLogOutput(t)

	if err := handleRequest(context.Background(), healthEvent(t, "123456789012", detail)); err != nil {
		t.Fatalf("handleRequest returned error: %v", err)
	}

	if len(*submissions) != 1 {
		t.Fatalf("recorded %d submissions, want 1: %+v", len(*submissions), *submissions)
	}
	got := (*submissions)[0]
	if got.Name != "aws.health.events.duration" {
		t.Errorf("submitted name = %q, want %q", got.Name, "aws.health.events.duration")
	}
	if got.Value != 5445 {
		t.Errorf("submitted value = %v, want 5445", got.Value)
	}

	wantTags := []string{
		"receiving_account:123456789012",
		"arn:" + detail.EventArn,
		"aws_service:EC2",
		"affected_region:af-south-1",
		"event_type_code:AWS_EC2_OPERATIONAL_ISSUE",
	}
	if len(got.Tags) != len(wantTags) {
		t.Fatalf("submitted %d tags, want %d: %q", len(got.Tags), len(wantTags), got.Tags)
	}
	for i := range wantTags {
		if got.Tags[i] != wantTags[i] {
			t.Errorf("tag %d = %q, want %q", i, got.Tags[i], wantTags[i])
		}
	}
}

func TestHandleRequestSubmitsZeroForEqualTimestamps(t *testing.T) {
	stamp := "Mon, 6 Jan 2025 10:00:00 GMT"
	detail := HealthEventDetail{
		EventArn:   "arn:aws:health:af-south-1::event/EC2/CODE/ID",
		StatusCode: statusCodeClosed,
		StartTime:  stamp,
		EndTime:    stamp,
	}

	submissions := recordSubmissions(t)
	captureLogOutput(t)

	if err := handleRequest(context.Background(), healthEvent(t, "123456789012", detail)); err != nil {
		t.Fatalf("handleRequest returned error: %v", err)
	}

	if len(*submissions) != 1 {
		t.Fatalf("recorded %d submissions, want 1: %+v", len(*submissions), *submissions)
	}
	if value := (*submissions)[0].Value; value != 0 {
		t.Errorf("submitted value = %v, want exactly 0", value)
	}
}

func TestHandleRequestLogsEverySubmissionExactlyOnce(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:      "arn:aws:health:us-east-1::event/RDS/AWS_RDS_OPERATIONAL_ISSUE/AWS_RDS_OPERATIONAL_ISSUE_1a2b3c4d",
		Service:       "RDS",
		EventTypeCode: "AWS_RDS_OPERATIONAL_ISSUE",
		StatusCode:    statusCodeClosed,
		StartTime:     "Mon, 6 Jan 2025 10:00:00 GMT",
		EndTime:       "Mon, 6 Jan 2025 10:01:00 GMT",
		EventRegion:   "us-east-1",
	}

	submissions := recordSubmissions(t)
	logs := captureLogOutput(t)

	if err := handleRequest(context.Background(), healthEvent(t, "123456789012", detail)); err != nil {
		t.Fatalf("handleRequest returned error: %v", err)
	}

	// Best-effort delivery: one attempt per invocation, never retried, and the
	// invocation still succeeds. ddlambda.Metric reports no failure, so the log
	// line is what keeps an unsubmitted sample reconstructible.
	if len(*submissions) != 1 {
		t.Fatalf("recorded %d submissions, want exactly 1: %+v", len(*submissions), *submissions)
	}

	var submitted []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.HasPrefix(line, "Submitted metric:") {
			submitted = append(submitted, line)
		}
	}
	if len(submitted) != 1 {
		t.Fatalf("got %d submission log lines, want 1:\n%s", len(submitted), logs.String())
	}

	for _, want := range []string{
		"name=aws.health.events.duration",
		"value=60",
		"eventArn=" + detail.EventArn,
	} {
		if !strings.Contains(submitted[0], want) {
			t.Errorf("submission log line %q does not contain %q", submitted[0], want)
		}
	}
}

// --- Failure-cause log line tests -------------------------------------------
//
// Requirement 3.3 allows exactly one log line naming the failure cause, and
// names exactly two causes. These tests count the lines that name a cause, not
// the total lines, because the handler also logs the envelope at invocation
// start.

// linesNamingCause returns the captured log lines that name the given failure
// cause, so a test can assert that exactly one line reports it.
func linesNamingCause(logs *bytes.Buffer, cause failureCause) []string {
	var matched []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, "cause="+string(cause)) {
			matched = append(matched, line)
		}
	}
	return matched
}

func TestHandleRequestLogsParseFailureCauseExactlyOnce(t *testing.T) {
	// A detail object whose eventArn is a number cannot unmarshal into
	// HealthEventDetail, and leaves the handler with no ARN to report.
	event := events.CloudWatchEvent{
		Version:    "0",
		DetailType: "AWS Health Event",
		Source:     "aws.health",
		AccountID:  "123456789012",
		Region:     "us-east-1",
		Detail:     json.RawMessage(`{"eventArn": 42, "statusCode": "closed"}`),
	}

	submissions := recordSubmissions(t)
	logs := captureLogOutput(t)

	err := handleRequest(context.Background(), event)
	if err == nil {
		t.Fatal("handleRequest returned nil for an unmarshalable detail, want error")
	}

	var perr *processingError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *processingError", err)
	}
	if perr.Cause != causeParseFailure {
		t.Errorf("cause = %q, want %q", perr.Cause, causeParseFailure)
	}
	if perr.EventArn != "" {
		t.Errorf("EventArn = %q, want empty (ARN unavailable)", perr.EventArn)
	}

	if len(*submissions) != 0 {
		t.Errorf("recorded %d submissions, want 0: %+v", len(*submissions), *submissions)
	}

	named := linesNamingCause(logs, causeParseFailure)
	if len(named) != 1 {
		t.Fatalf("got %d log lines naming cause %q, want 1:\n%s", len(named), causeParseFailure, logs.String())
	}
	if !strings.Contains(named[0], arnUnavailableNote) {
		t.Errorf("parse-failure log line %q does not record %q", named[0], arnUnavailableNote)
	}
	// The other cause must not appear anywhere in the output.
	if other := linesNamingCause(logs, causeBadTimestamp); len(other) != 0 {
		t.Errorf("got %d log lines naming cause %q, want 0:\n%s", len(other), causeBadTimestamp, logs.String())
	}
}

func TestHandleRequestLogsBadTimestampCauseExactlyOnce(t *testing.T) {
	detail := HealthEventDetail{
		EventArn:      "arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae",
		Service:       "EC2",
		EventTypeCode: "AWS_EC2_OPERATIONAL_ISSUE",
		StatusCode:    statusCodeClosed,
		StartTime:     "2025-01-06T10:00:00Z", // ISO-8601, rejected by the AWS Health layout
		EndTime:       "",                     // absent
		EventRegion:   "af-south-1",
	}

	submissions := recordSubmissions(t)
	logs := captureLogOutput(t)

	err := handleRequest(context.Background(), healthEvent(t, "123456789012", detail))
	if err == nil {
		t.Fatal("handleRequest returned nil for a closed event with a bad timestamp, want error")
	}

	var perr *processingError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *processingError", err)
	}
	if perr.Cause != causeBadTimestamp {
		t.Errorf("cause = %q, want %q", perr.Cause, causeBadTimestamp)
	}
	if perr.EventArn != detail.EventArn {
		t.Errorf("EventArn = %q, want %q", perr.EventArn, detail.EventArn)
	}

	if len(*submissions) != 0 {
		t.Errorf("recorded %d submissions, want 0: %+v", len(*submissions), *submissions)
	}

	named := linesNamingCause(logs, causeBadTimestamp)
	if len(named) != 1 {
		t.Fatalf("got %d log lines naming cause %q, want 1:\n%s", len(named), causeBadTimestamp, logs.String())
	}
	for _, want := range []string{
		"eventArn=" + detail.EventArn,
		"startTime=" + strconv.Quote(detail.StartTime),
		"endTime=" + strconv.Quote(detail.EndTime),
	} {
		if !strings.Contains(named[0], want) {
			t.Errorf("bad-timestamp log line %q does not contain %q", named[0], want)
		}
	}
	// The other cause must not appear anywhere in the output.
	if other := linesNamingCause(logs, causeParseFailure); len(other) != 0 {
		t.Errorf("got %d log lines naming cause %q, want 0:\n%s", len(other), causeParseFailure, logs.String())
	}
}
