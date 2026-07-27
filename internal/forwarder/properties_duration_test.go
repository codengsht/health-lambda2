package forwarder

// Property tests over the Forwarder's duration arithmetic and status gating.
//
// Requirements: 1.1, 1.6, 1.7, 2.9, 3.5

import (
	"flag"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Feature: aws-health-event-telemetry, Property 1: Closed events with parseable timestamps yield exactly one correctly valued sample
//
// For any EventBridge account id, any Health_Event_Detail whose statusCode is
// exactly "closed", and any pair of UTC instants at whole-second granularity
// rendered with the AWS Health RFC2822 layout — including equal instants and
// empty values in the other four source fields — evaluation returns no error
// and exactly one sample named aws.health.events.duration whose value is the
// signed number of seconds of endTime minus startTime, with no rounding,
// clamping, unit conversion, or default substitution.
//
// Validates: Requirements 1.1, 1.7, 2.9, 3.5
func TestPropertyClosedEventWithParseableTimestampsYieldsOneCorrectlyValuedSample(t *testing.T) {
	// Explicit floor of 100 iterations, independent of rapid's default and of
	// any lower value supplied through -rapid.checks or RAPID_CHECKS.
	const minIterations = 100
	if f := flag.Lookup("rapid.checks"); f != nil {
		if getter, ok := f.Value.(flag.Getter); ok {
			if configured, ok := getter.Get().(int); ok && configured < minIterations {
				if err := f.Value.Set("100"); err != nil {
					t.Fatalf("could not raise the iteration count to %d: %v", minIterations, err)
				}
			}
		}
	}

	rapid.Check(t, func(t *rapid.T) {
		accountID := genAccountID().Draw(t, "accountID")
		pair := genInstantPair().Draw(t, "instantPair")
		detail := drawDetail(t, "", statusCodeClosed, pair.StartRaw, pair.EndRaw)

		result, err := evaluate(accountID, detail)
		if err != nil {
			t.Fatalf("evaluate(%q, %+v) returned error %v, want nil (Requirement 3.5)", accountID, detail, err)
		}

		if result.Sample == nil {
			t.Fatalf("evaluate(%q, %+v) produced no sample, want exactly one (Requirements 1.1, 2.9)", accountID, detail)
		}

		if result.Sample.Name != metricNameDuration {
			t.Fatalf("sample name = %q, want %q (Requirement 1.1)", result.Sample.Name, metricNameDuration)
		}

		// The delta is recomputed from the instants themselves rather than read
		// back from the generator, so a rounding, clamping, or unit-conversion
		// bug cannot hide behind a shared computation.
		want := float64(pair.End.Unix() - pair.Start.Unix())
		if result.Sample.Value != want {
			t.Fatalf("sample value = %v for startTime=%q endTime=%q, want the signed second delta %v (Requirement 1.1)",
				result.Sample.Value, pair.StartRaw, pair.EndRaw, want)
		}
		if result.Sample.Value != pair.DeltaSeconds {
			t.Fatalf("sample value = %v, want %v as rendered by the layout (Requirement 1.1)",
				result.Sample.Value, pair.DeltaSeconds)
		}

		// Equal instants must give exactly 0, with no default substitution.
		if pair.Start.Equal(pair.End) && result.Sample.Value != 0 {
			t.Fatalf("sample value = %v for equal instants %q, want 0 (Requirement 1.7)",
				result.Sample.Value, pair.StartRaw)
		}
	})
}

// Feature: aws-health-event-telemetry, Property 3: The AWS Health time layout round-trips UTC instants
//
// For any UTC instant truncated to whole seconds, formatting it with the layout
// "Mon, 2 Jan 2006 15:04:05 GMT" and parsing the result with the same layout
// returns an instant equal to the original, and the parsed value carries no
// non-UTC zone offset.
//
// Validates: Requirements 1.6
func TestPropertyHealthEventTimeFormatRoundTripsUTCInstants(t *testing.T) {
	// Explicit floor of 100 iterations, independent of rapid's default and of
	// any lower value supplied through -rapid.checks or RAPID_CHECKS.
	const minIterations = 100
	if f := flag.Lookup("rapid.checks"); f != nil {
		if getter, ok := f.Value.(flag.Getter); ok {
			if configured, ok := getter.Get().(int); ok && configured < minIterations {
				if err := f.Value.Set("100"); err != nil {
					t.Fatalf("could not raise the iteration count to %d: %v", minIterations, err)
				}
			}
		}
	}

	rapid.Check(t, func(t *rapid.T) {
		original := genInstant().Draw(t, "instant").UTC().Truncate(time.Second)

		rendered := original.Format(healthEventTimeFormat)

		parsed, err := time.Parse(healthEventTimeFormat, rendered)
		if err != nil {
			t.Fatalf("time.Parse(%q, %q) returned error %v, want the layout to accept its own rendering (Requirement 1.6)",
				healthEventTimeFormat, rendered, err)
		}

		if !parsed.Equal(original) {
			t.Fatalf("round-trip of %q through %q gave %q, want the same instant (Requirement 1.6)",
				original, healthEventTimeFormat, parsed)
		}
		// Unix seconds are compared as well, so an equality that holds only
		// through a compensating zone offset cannot pass unnoticed.
		if parsed.Unix() != original.Unix() {
			t.Fatalf("round-trip of %q gave unix second %d, want %d (Requirement 1.6)",
				rendered, parsed.Unix(), original.Unix())
		}

		// The parsed value must be a UTC instant: no offset from UTC, and no
		// location that could shift a later rendering or comparison.
		if _, offset := parsed.Zone(); offset != 0 {
			t.Fatalf("parsing %q gave zone offset %d seconds, want 0 so the value is a UTC instant (Requirement 1.6)",
				rendered, offset)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("parsing %q gave location %v, want time.UTC (Requirement 1.6)", rendered, parsed.Location())
		}

		// A UTC instant at whole-second granularity survives the layout with no
		// sub-second residue, which is what makes the second delta integral.
		if parsed.Nanosecond() != 0 {
			t.Fatalf("parsing %q gave nanosecond %d, want 0 (Requirement 1.6)", rendered, parsed.Nanosecond())
		}
	})
}

// Feature: aws-health-event-telemetry, Property 4: An inverted time range emits the negative value and reports it
//
// For any pair of UTC instants where the end instant precedes the start
// instant, evaluation of a closed event carrying them returns no error, a
// sample whose value is the unchanged negative second delta, and exactly one
// inverted-time-range notice carrying the received startTime value, the
// received endTime value, and the eventArn value.
//
// Validates: Requirements 1.5
func TestPropertyInvertedTimeRangeEmitsTheNegativeValueAndReportsIt(t *testing.T) {
	// Explicit floor of 100 iterations, independent of rapid's default and of
	// any lower value supplied through -rapid.checks or RAPID_CHECKS.
	const minIterations = 100
	if f := flag.Lookup("rapid.checks"); f != nil {
		if getter, ok := f.Value.(flag.Getter); ok {
			if configured, ok := getter.Get().(int); ok && configured < minIterations {
				if err := f.Value.Set("100"); err != nil {
					t.Fatalf("could not raise the iteration count to %d: %v", minIterations, err)
				}
			}
		}
	}

	rapid.Check(t, func(t *rapid.T) {
		accountID := genAccountID().Draw(t, "accountID")
		pair := genInvertedInstantPair().Draw(t, "invertedInstantPair")
		detail := drawDetail(t, "", statusCodeClosed, pair.StartRaw, pair.EndRaw)

		result, err := evaluate(accountID, detail)
		if err != nil {
			t.Fatalf("evaluate(%q, %+v) returned error %v, want nil for an inverted range (Requirement 1.5)",
				accountID, detail, err)
		}

		if result.Sample == nil {
			t.Fatalf("evaluate(%q, %+v) produced no sample, want the negative value submitted (Requirement 1.5)",
				accountID, detail)
		}

		// The delta is recomputed from the instants themselves, so a clamping,
		// math.Abs, or default-substitution bug cannot hide behind a value
		// carried over from the generator.
		want := float64(pair.End.Unix() - pair.Start.Unix())
		if want >= 0 {
			t.Fatalf("generated pair startTime=%q endTime=%q is not inverted (delta %v)",
				pair.StartRaw, pair.EndRaw, want)
		}
		if result.Sample.Value != want {
			t.Fatalf("sample value = %v for startTime=%q endTime=%q, want the unchanged negative delta %v (Requirement 1.5)",
				result.Sample.Value, pair.StartRaw, pair.EndRaw, want)
		}
		if result.Sample.Value >= 0 {
			t.Fatalf("sample value = %v, want a negative value for an inverted range (Requirement 1.5)",
				result.Sample.Value)
		}

		// Exactly one inverted-range notice, carrying the three received values
		// verbatim. Other notice kinds (an over-long arn tag, for instance) may
		// accompany it, so only the inverted-range notices are counted.
		var inverted []notice
		for _, n := range result.Notices {
			if n.Kind == noticeInvertedRange {
				inverted = append(inverted, n)
			}
		}
		if len(inverted) != 1 {
			t.Fatalf("got %d %s notices in %+v, want exactly 1 (Requirement 1.5)",
				len(inverted), noticeInvertedRange, result.Notices)
		}

		reported := inverted[0].Fields
		if got := reported["startTime"]; got != detail.StartTime {
			t.Fatalf("inverted-range notice startTime = %q, want the received %q (Requirement 1.5)",
				got, detail.StartTime)
		}
		if got := reported["endTime"]; got != detail.EndTime {
			t.Fatalf("inverted-range notice endTime = %q, want the received %q (Requirement 1.5)",
				got, detail.EndTime)
		}
		if got := reported["eventArn"]; got != detail.EventArn {
			t.Fatalf("inverted-range notice eventArn = %q, want the received %q (Requirement 1.5)",
				got, detail.EventArn)
		}
	})
}

// genArbitraryTimestamp draws a startTime or endTime value with no constraint
// on whether it parses: absent/empty values, values the AWS Health layout
// rejects, and values the layout accepts are all reachable. Property 7 needs
// this because a non-closed status must be gated before any timestamp is read.
func genArbitraryTimestamp() *rapid.Generator[string] {
	return rapid.OneOf(
		genBadTimestamp(),
		rapid.Just(""),
		rapid.Map(genInstant(), func(at time.Time) string {
			return at.Format(healthEventTimeFormat)
		}),
	)
}

// Feature: aws-health-event-telemetry, Property 7: A non-closed status never errors and never emits
//
// For any Health_Event_Detail whose statusCode is any string other than the
// exact lowercase "closed" — including the empty string, "open", "upcoming",
// "Closed", "CLOSED", and arbitrary unicode — and any startTime and endTime
// values whatsoever, including absent, empty, and unparseable ones, evaluation
// returns no error and no metric sample.
//
// Validates: Requirements 1.3, 3.6
func TestPropertyNonClosedStatusNeverErrorsAndNeverEmits(t *testing.T) {
	// Explicit floor of 100 iterations, independent of rapid's default and of
	// any lower value supplied through -rapid.checks or RAPID_CHECKS.
	const minIterations = 100
	if f := flag.Lookup("rapid.checks"); f != nil {
		if getter, ok := f.Value.(flag.Getter); ok {
			if configured, ok := getter.Get().(int); ok && configured < minIterations {
				if err := f.Value.Set("100"); err != nil {
					t.Fatalf("could not raise the iteration count to %d: %v", minIterations, err)
				}
			}
		}
	}

	rapid.Check(t, func(t *rapid.T) {
		accountID := genAccountID().Draw(t, "accountID")
		statusCode := genNonClosedStatusCode().Draw(t, "statusCode")
		startRaw := genArbitraryTimestamp().Draw(t, "startTime")
		endRaw := genArbitraryTimestamp().Draw(t, "endTime")

		// Guard the generator's own contract: a drawn "closed" would make the
		// property vacuous rather than failing it.
		if statusCode == statusCodeClosed {
			t.Fatalf("drew the closed status %q, which this property excludes", statusCode)
		}

		detail := drawDetail(t, "", statusCode, startRaw, endRaw)

		result, err := evaluate(accountID, detail)
		if err != nil {
			t.Fatalf("evaluate(%q, %+v) returned error %v, want nil for statusCode %q (Requirements 1.3, 3.6)",
				accountID, detail, err, statusCode)
		}

		if result.Sample != nil {
			t.Fatalf("evaluate(%q, %+v) produced sample %+v for statusCode %q, want no sample (Requirement 1.3)",
				accountID, detail, *result.Sample, statusCode)
		}

		// The skip must be reported exactly once, carrying the received status
		// and ARN, so a silently dropped delivery is distinguishable from a
		// processed one in CloudWatch Logs.
		var skipped []notice
		for _, n := range result.Notices {
			if n.Kind == noticeNonClosedSkipped {
				skipped = append(skipped, n)
			}
		}
		if len(skipped) != 1 {
			t.Fatalf("got %d %s notices in %+v for statusCode %q, want exactly 1 (Requirement 1.3)",
				len(skipped), noticeNonClosedSkipped, result.Notices, statusCode)
		}
		if got := skipped[0].Fields["statusCode"]; got != statusCode {
			t.Fatalf("non-closed notice statusCode = %q, want the received %q (Requirement 1.3)", got, statusCode)
		}
		if got := skipped[0].Fields["eventArn"]; got != detail.EventArn {
			t.Fatalf("non-closed notice eventArn = %q, want the received %q (Requirement 1.3)", got, detail.EventArn)
		}

		// A non-closed delivery is gated before any timestamp is read, so no
		// timestamp-derived notice may appear regardless of what the timestamps
		// hold (Requirement 3.6).
		for _, n := range result.Notices {
			if n.Kind != noticeNonClosedSkipped {
				t.Fatalf("got notice %+v for statusCode %q with startTime=%q endTime=%q, want only the non-closed skip (Requirement 3.6)",
					n, statusCode, startRaw, endRaw)
			}
		}
	})
}
