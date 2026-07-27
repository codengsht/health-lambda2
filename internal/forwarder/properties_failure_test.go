package forwarder

// Property tests for the Forwarder's failure paths.
//
// Requirements: 3.1, 3.2, 3.3

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"pgregory.net/rapid"
)

// minChecksProperty5 is the explicit iteration floor for Property 5. rapid's
// default check count already meets it; the constant keeps the floor visible in
// the source and enforced regardless of how the suite is invoked.
const minChecksProperty5 = 100

// ensureMinimumChecksProperty5 raises rapid's check count to
// minChecksProperty5 when the ambient count is lower, restoring the previous
// value when the test finishes.
func ensureMinimumChecksProperty5(t *testing.T) {
	t.Helper()

	f := flag.Lookup("rapid.checks")
	if f == nil {
		return
	}

	previous := f.Value.String()
	if current, err := strconv.Atoi(previous); err != nil || current >= minChecksProperty5 {
		return
	}

	if err := flag.Set("rapid.checks", strconv.Itoa(minChecksProperty5)); err != nil {
		t.Fatalf("could not raise rapid.checks to %d: %v", minChecksProperty5, err)
	}
	t.Cleanup(func() { _ = flag.Set("rapid.checks", previous) })
}

// Feature: aws-health-event-telemetry, Property 5: A closed event with an absent, empty, or unparseable timestamp always fails with the timestamp cause
//
// For any Health_Event_Detail whose statusCode is exactly "closed" and whose
// startTime or endTime is absent, is the empty string, or is any string the
// layout "Mon, 2 Jan 2006 15:04:05 GMT" rejects, evaluation returns no metric
// sample and returns an error whose named cause is
// "missing/unparseable timestamp" and whose ARN field equals the received
// detail.eventArn.
//
// **Validates: Requirements 3.2, 3.3**
func TestProperty5ClosedEventWithBadTimestampAlwaysFailsWithTimestampCause(t *testing.T) {
	ensureMinimumChecksProperty5(t)

	rapid.Check(t, func(t *rapid.T) {
		// A parseable pair supplies the field that stays valid, so the failure
		// is attributable to the bad value alone.
		parseable := genInstantPair().Draw(t, "parseableInstantPair")
		startRaw, endRaw := parseable.StartRaw, parseable.EndRaw

		switch rapid.SampledFrom([]string{"start", "end", "both"}).Draw(t, "badTimestampField") {
		case "start":
			startRaw = genBadTimestamp().Draw(t, "badStartTime")
		case "end":
			endRaw = genBadTimestamp().Draw(t, "badEndTime")
		default:
			startRaw = genBadTimestamp().Draw(t, "badStartTimeBoth")
			endRaw = genBadTimestamp().Draw(t, "badEndTimeBoth")
		}

		accountID := genAccountID().Draw(t, "accountID")
		detail := drawDetail(t, "closed/", statusCodeClosed, startRaw, endRaw)

		result, err := evaluate(accountID, detail)

		if err == nil {
			t.Fatalf("evaluate returned no error for startTime=%q endTime=%q", startRaw, endRaw)
		}
		if result.Sample != nil {
			t.Fatalf("evaluate returned sample %+v for startTime=%q endTime=%q, want none",
				*result.Sample, startRaw, endRaw)
		}

		var perr *processingError
		if !errors.As(err, &perr) {
			t.Fatalf("evaluate returned %T (%v), want a *processingError", err, err)
		}
		if perr.Cause != causeBadTimestamp {
			t.Fatalf("failure cause = %q, want %q", perr.Cause, causeBadTimestamp)
		}
		if string(perr.Cause) != "missing/unparseable timestamp" {
			t.Fatalf("named failure cause = %q, want %q", perr.Cause, "missing/unparseable timestamp")
		}
		if perr.EventArn != detail.EventArn {
			t.Fatalf("error ARN field = %q, want the received eventArn %q", perr.EventArn, detail.EventArn)
		}
	})
}

// minChecksProperty6 is the explicit iteration floor for Property 6, kept
// visible in the source so the floor holds however the suite is invoked.
const minChecksProperty6 = 100

// ensureMinimumChecksProperty6 raises rapid's check count to
// minChecksProperty6 when the ambient count is lower, restoring the previous
// value when the test finishes.
func ensureMinimumChecksProperty6(t *testing.T) {
	t.Helper()

	f := flag.Lookup("rapid.checks")
	if f == nil {
		return
	}

	previous := f.Value.String()
	if current, err := strconv.Atoi(previous); err != nil || current >= minChecksProperty6 {
		return
	}

	if err := flag.Set("rapid.checks", strconv.Itoa(minChecksProperty6)); err != nil {
		t.Fatalf("could not raise rapid.checks to %d: %v", minChecksProperty6, err)
	}
	t.Cleanup(func() { _ = flag.Set("rapid.checks", previous) })
}

// Feature: aws-health-event-telemetry, Property 6: An unmarshalable detail payload always fails with the parse cause and submits nothing
//
// For any byte sequence that encoding/json cannot unmarshal into a
// HealthEventDetail, handling that payload records zero metric submissions on
// the submission sink and returns an error whose named cause is "parse failure",
// recording that the ARN is unavailable.
//
// The property runs through Handle — the only place a submission can
// happen — with submitMetric swapped for a recording fake, so "submits nothing"
// is asserted against the sink rather than inferred from the pure core.
//
// **Validates: Requirements 3.1, 3.3**
func TestProperty6UnmarshalableDetailAlwaysFailsWithParseCauseAndSubmitsNothing(t *testing.T) {
	ensureMinimumChecksProperty6(t)

	rapid.Check(t, func(t *rapid.T) {
		payload := genMalformedDetail().Draw(t, "malformedDetail")
		accountID := genAccountID().Draw(t, "accountID")

		// The fake sink and the log buffer are both restored by t.Cleanup at the
		// end of this iteration, so iterations cannot observe each other.
		submissions := recordSubmissions(t)
		logs := captureLogOutput(t)

		event := events.CloudWatchEvent{
			Version:    "0",
			ID:         "0f5c3f0f-1b0a-4e63-9a1a-0c0a1c2f3d4e",
			DetailType: "AWS Health Event",
			Source:     "aws.health",
			AccountID:  accountID,
			Region:     "us-east-1",
			Detail:     payload,
		}

		err := Handle(context.Background(), event)

		if err == nil {
			t.Fatalf("Handle returned no error for detail %q", payload)
		}
		if len(*submissions) != 0 {
			t.Fatalf("recorded %d submissions for detail %q, want 0: %+v",
				len(*submissions), payload, *submissions)
		}

		var perr *processingError
		if !errors.As(err, &perr) {
			t.Fatalf("Handle returned %T (%v), want a *processingError", err, err)
		}
		if perr.Cause != causeParseFailure {
			t.Fatalf("failure cause = %q, want %q", perr.Cause, causeParseFailure)
		}
		if string(perr.Cause) != "parse failure" {
			t.Fatalf("named failure cause = %q, want %q", perr.Cause, "parse failure")
		}
		if perr.EventArn != "" {
			t.Fatalf("error carried eventArn %q, want none: no ARN can be read from an unmarshalable detail",
				perr.EventArn)
		}
		if !strings.Contains(err.Error(), arnUnavailableNote) {
			t.Fatalf("error %q does not record that the ARN is unavailable (%q)", err.Error(), arnUnavailableNote)
		}

		// Requirement 3.3: exactly one failure entry, naming the cause and
		// recording the ARN as unavailable.
		var failures []string
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			if strings.HasPrefix(line, "Failing invocation:") {
				failures = append(failures, line)
			}
		}
		if len(failures) != 1 {
			t.Fatalf("got %d failure log entries for detail %q, want 1:\n%s", len(failures), payload, logs.String())
		}
		if !strings.Contains(failures[0], "cause="+string(causeParseFailure)) {
			t.Fatalf("failure log entry %q does not name cause %q", failures[0], causeParseFailure)
		}
		if !strings.Contains(failures[0], arnUnavailableNote) {
			t.Fatalf("failure log entry %q does not record %q", failures[0], arnUnavailableNote)
		}
	})
}

// minChecksProperty8 is the explicit iteration floor for Property 8, kept
// visible in the source so the floor holds however the suite is invoked.
const minChecksProperty8 = 100

// ensureMinimumChecksProperty8 raises rapid's check count to
// minChecksProperty8 when the ambient count is lower, restoring the previous
// value when the test finishes.
func ensureMinimumChecksProperty8(t *testing.T) {
	t.Helper()

	f := flag.Lookup("rapid.checks")
	if f == nil {
		return
	}

	previous := f.Value.String()
	if current, err := strconv.Atoi(previous); err != nil || current >= minChecksProperty8 {
		return
	}

	if err := flag.Set("rapid.checks", strconv.Itoa(minChecksProperty8)); err != nil {
		t.Fatalf("could not raise rapid.checks to %d: %v", minChecksProperty8, err)
	}
	t.Cleanup(func() { _ = flag.Set("rapid.checks", previous) })
}

// property8Payload is one Event_Payload reduced to the two values evaluation
// reads: the EventBridge envelope account and the decoded detail.
type property8Payload struct {
	AccountID string
	Detail    HealthEventDetail
}

// drawProperty8Payload draws a payload from all three evaluation outcomes:
// a closed event with parseable timestamps (sample plus any notices), a closed
// event with a rejected timestamp (named failure cause), and a non-closed
// event (no sample, one notice).
func drawProperty8Payload(t *rapid.T, label string) property8Payload {
	accountID := genAccountID().Draw(t, label+"/accountID")

	switch rapid.SampledFrom([]string{"closedParseable", "closedBadTimestamp", "nonClosed"}).
		Draw(t, label+"/shape") {
	case "closedParseable":
		pair := genInstantPair().Draw(t, label+"/instantPair")
		return property8Payload{
			AccountID: accountID,
			Detail:    drawDetail(t, label+"/", statusCodeClosed, pair.StartRaw, pair.EndRaw),
		}
	case "closedBadTimestamp":
		pair := genInstantPair().Draw(t, label+"/parseableInstantPair")
		startRaw, endRaw := pair.StartRaw, pair.EndRaw
		switch rapid.SampledFrom([]string{"start", "end", "both"}).Draw(t, label+"/badField") {
		case "start":
			startRaw = genBadTimestamp().Draw(t, label+"/badStartTime")
		case "end":
			endRaw = genBadTimestamp().Draw(t, label+"/badEndTime")
		default:
			startRaw = genBadTimestamp().Draw(t, label+"/badStartTimeBoth")
			endRaw = genBadTimestamp().Draw(t, label+"/badEndTimeBoth")
		}
		return property8Payload{
			AccountID: accountID,
			Detail:    drawDetail(t, label+"/", statusCodeClosed, startRaw, endRaw),
		}
	default:
		status := genNonClosedStatusCode().Draw(t, label+"/statusCode")
		startRaw := rapid.OneOf(
			rapid.Map(genInstantPair(), func(p instantPair) string { return p.StartRaw }),
			genBadTimestamp(),
		).Draw(t, label+"/startTime")
		endRaw := rapid.OneOf(
			rapid.Map(genInstantPair(), func(p instantPair) string { return p.EndRaw }),
			genBadTimestamp(),
		).Draw(t, label+"/endTime")
		return property8Payload{
			AccountID: accountID,
			Detail:    drawDetail(t, label+"/", status, startRaw, endRaw),
		}
	}
}

// property8Outcome is everything Property 8 requires to be identical between
// an isolated first evaluation and any later evaluation of the same payload:
// the metric name, the metric value with no numeric difference (compared as
// raw bits, so a sign-of-zero or precision difference cannot hide), the five
// tag strings, the notices, and the named failure cause.
type property8Outcome struct {
	HasSample bool
	Name      string
	ValueBits uint64
	Tags      []string
	Notices   []notice
	HasError  bool
	Cause     failureCause
	ErrorArn  string
	ErrorText string
}

// captureProperty8Outcome records the observable outcome of one evaluation.
func captureProperty8Outcome(result evaluation, err error) property8Outcome {
	outcome := property8Outcome{Notices: result.Notices}

	if result.Sample != nil {
		outcome.HasSample = true
		outcome.Name = result.Sample.Name
		outcome.ValueBits = math.Float64bits(result.Sample.Value)
		outcome.Tags = result.Sample.Tags
	}

	if err != nil {
		outcome.HasError = true
		outcome.ErrorText = err.Error()
		var perr *processingError
		if errors.As(err, &perr) {
			outcome.Cause = perr.Cause
			outcome.ErrorArn = perr.EventArn
		}
	}

	return outcome
}

// property8Difference reports the first way in which a later outcome differs
// from the isolated baseline outcome, or "" when the two are identical.
func property8Difference(baseline, got property8Outcome) string {
	if baseline.HasError != got.HasError {
		return fmt.Sprintf("error presence: baseline hasError=%t, got hasError=%t (baseline %q, got %q)",
			baseline.HasError, got.HasError, baseline.ErrorText, got.ErrorText)
	}
	if baseline.Cause != got.Cause {
		return fmt.Sprintf("named failure cause: baseline %q, got %q", baseline.Cause, got.Cause)
	}
	if baseline.ErrorArn != got.ErrorArn {
		return fmt.Sprintf("failure ARN field: baseline %q, got %q", baseline.ErrorArn, got.ErrorArn)
	}
	if baseline.ErrorText != got.ErrorText {
		return fmt.Sprintf("failure text: baseline %q, got %q", baseline.ErrorText, got.ErrorText)
	}

	if baseline.HasSample != got.HasSample {
		return fmt.Sprintf("sample presence: baseline hasSample=%t, got hasSample=%t",
			baseline.HasSample, got.HasSample)
	}
	if baseline.Name != got.Name {
		return fmt.Sprintf("metric name: baseline %q, got %q", baseline.Name, got.Name)
	}
	if baseline.ValueBits != got.ValueBits {
		return fmt.Sprintf("metric value: baseline %v (bits %#x), got %v (bits %#x)",
			math.Float64frombits(baseline.ValueBits), baseline.ValueBits,
			math.Float64frombits(got.ValueBits), got.ValueBits)
	}
	if len(baseline.Tags) != len(got.Tags) {
		return fmt.Sprintf("tag count: baseline %d (%q), got %d (%q)",
			len(baseline.Tags), baseline.Tags, len(got.Tags), got.Tags)
	}
	for i := range baseline.Tags {
		if baseline.Tags[i] != got.Tags[i] {
			return fmt.Sprintf("tag %d: baseline %q, got %q", i, baseline.Tags[i], got.Tags[i])
		}
	}

	if len(baseline.Notices) != len(got.Notices) {
		return fmt.Sprintf("notice count: baseline %d (%+v), got %d (%+v)",
			len(baseline.Notices), baseline.Notices, len(got.Notices), got.Notices)
	}
	for i := range baseline.Notices {
		if baseline.Notices[i].Kind != got.Notices[i].Kind {
			return fmt.Sprintf("notice %d kind: baseline %q, got %q",
				i, baseline.Notices[i].Kind, got.Notices[i].Kind)
		}
		if !maps.Equal(baseline.Notices[i].Fields, got.Notices[i].Fields) {
			return fmt.Sprintf("notice %d fields: baseline %v, got %v",
				i, baseline.Notices[i].Fields, got.Notices[i].Fields)
		}
	}

	return ""
}

// Feature: aws-health-event-telemetry, Property 8: Evaluation is a pure function of the single payload
//
// For any sequence of Event_Payloads in any order, with any duplicates and
// interleavings, and for any number of repeated evaluations within one process,
// the outcome produced for a given payload — the metric name, the metric value
// with no numeric difference, the five tag strings character-for-character, the
// notices, and the named failure cause when the payload fails — is identical to
// the outcome produced when that payload is evaluated in isolation as the first
// payload of a fresh process.
//
// The baseline for each payload is its isolated first evaluation, taken before
// any other payload is evaluated in that iteration; the arbitrary sequence that
// follows then reorders, duplicates, and repeats those payloads and every
// outcome is compared back to the baseline. Nothing in the decision core reads
// process state, so a within-process divergence is the only way the
// fresh-process claim could break.
//
// **Validates: Requirements 2.7, 3.7, 4.1, 4.3, 4.9, 7.7**
func TestProperty8EvaluationIsAPureFunctionOfTheSinglePayload(t *testing.T) {
	ensureMinimumChecksProperty8(t)

	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(1, 4).Draw(t, "payloadCount")

		payloads := make([]property8Payload, count)
		for i := range payloads {
			payloads[i] = drawProperty8Payload(t, "payload"+strconv.Itoa(i))
		}

		// Isolated first evaluation of each payload: the baseline the property
		// compares every later evaluation against.
		baselines := make([]property8Outcome, count)
		for i, p := range payloads {
			baselines[i] = captureProperty8Outcome(evaluate(p.AccountID, p.Detail))
			if baselines[i].HasSample && len(baselines[i].Tags) != 5 {
				t.Fatalf("payload %d produced %d tags, want the five tag strings: %q",
					i, len(baselines[i].Tags), baselines[i].Tags)
			}
		}

		// An arbitrary sequence over the same payloads: any order, duplicates
		// and interleavings included, with each step evaluated one or more
		// times in a row.
		sequence := rapid.SliceOfN(rapid.IntRange(0, count-1), 1, 12).Draw(t, "sequence")
		for step, index := range sequence {
			repeats := rapid.IntRange(1, 3).Draw(t, "repeats"+strconv.Itoa(step))
			for r := 0; r < repeats; r++ {
				got := captureProperty8Outcome(evaluate(payloads[index].AccountID, payloads[index].Detail))
				if diff := property8Difference(baselines[index], got); diff != "" {
					t.Fatalf("payload %d diverged from its isolated first evaluation at sequence step %d repeat %d: %s\npayload: accountID=%q detail=%+v",
						index, step, r, diff, payloads[index].AccountID, payloads[index].Detail)
				}
			}
		}
	})
}
