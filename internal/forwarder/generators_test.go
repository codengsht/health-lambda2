package forwarder

// Rapid generators shared by every property test in this package.
//
// The design's generator table (Testing Strategy) defines six generators and
// the input space each one must cover. They live here so the nine property
// tests in properties_duration_test.go, properties_tags_test.go, and
// properties_failure_test.go all draw from the same definitions:
//
//	genFieldValue         verbatim tag source values: empty, whitespace-only,
//	                      mixed case, unicode, 250+ character strings
//	genEventArn           realistic AWS Health ARNs, zero/one/many "/", values
//	                      pushing "arn:<value>" below, at, and above 200 chars
//	genStatusCode         "closed" plus non-closed values (see
//	                      genNonClosedStatusCode for the filtered variant)
//	genInstantPair        ordered, equal, and inverted UTC instant pairs at
//	                      whole-second granularity, formatted with the layout
//	                      (see genInvertedInstantPair for inverted-only draws)
//	genBadTimestamp       "", ISO-8601, RFC3339, layout with a missing
//	                      component, non-date noise, valid-shaped invalid dates
//	genMalformedDetail    detail bytes encoding/json cannot unmarshal into
//	                      HealthEventDetail
//
// Two convenience helpers sit alongside them: genAccountID for the EventBridge
// envelope account value, and drawDetail, which assembles a HealthEventDetail
// from generated parts given a status code and the two raw timestamps.
//
// Requirements: 1.1, 2.9, 3.2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"pgregory.net/rapid"
)

// --- Verbatim field values ---------------------------------------------------

// whitespaceOnlyValues are values that must survive untrimmed (Requirement 2.3).
var whitespaceOnlyValues = []string{" ", "  ", "\t", "\n", " \t\n ", "\u00a0"}

// mixedCaseValues keep case-sensitivity observable (Requirements 2.3, 2.6).
var mixedCaseValues = []string{
	"EC2", "ec2", "Ec2", "MULTIPLE_SERVICES", "multiple_services",
	"AWS_EC2_OPERATIONAL_ISSUE", "aws_ec2_operational_issue",
	"AWS_Ec2_Operational_Issue", "af-south-1", "AF-SOUTH-1", " EC2 ",
}

// unicodeValues carry multi-byte runes so byte-for-byte copying is tested
// rather than rune-for-rune copying.
var unicodeValues = []string{
	"ünïcodé", "日本語サービス", "Ωμέγα", "региона", "emoji-🚀-service", "e\u0301c2",
}

// genFieldValue draws a value for one of the four non-ARN tag source fields:
// the envelope account, detail.service, detail.eventRegion, and
// detail.eventTypeCode. It covers the empty string, whitespace-only values,
// mixed case, unicode, and strings of 250 characters or more.
func genFieldValue() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.Just(""),
		rapid.SampledFrom(whitespaceOnlyValues),
		rapid.SampledFrom(mixedCaseValues),
		rapid.SampledFrom(unicodeValues),
		genLongValue(),
		rapid.String(),
		rapid.StringOf(rapid.RuneFrom(nil, unicode.Latin, unicode.Cyrillic, unicode.Han)),
	)
}

// genLongValue draws strings of 250 characters or more, so tag strings past
// Datadog's 200-character limit are reachable through any field.
func genLongValue() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		n := rapid.IntRange(250, 400).Draw(t, "longValueLength")
		unit := rapid.SampledFrom([]string{"a", "Z", "_", "é", "服"}).Draw(t, "longValueUnit")
		return strings.Repeat(unit, n)
	})
}

// genAccountID draws an EventBridge envelope account value: realistic 12-digit
// AWS account ids plus the odd values genFieldValue covers, since Requirement
// 2.9 allows an absent or empty account.
func genAccountID() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.StringOfN(rapid.RuneFrom([]rune("0123456789")), 12, 12, 12),
		genFieldValue(),
	)
}

// --- Event ARNs -------------------------------------------------------------

// arnRegions and arnServices feed the realistic ARN shape.
var (
	arnRegions  = []string{"af-south-1", "us-east-1", "eu-west-3", "ap-southeast-2", "global"}
	arnServices = []string{"EC2", "MULTIPLE_SERVICES", "RDS", "LAMBDA", "CLOUDFRONT"}
	arnCodes    = []string{
		"AWS_EC2_OPERATIONAL_ISSUE",
		"AWS_RDS_OPERATIONAL_ISSUE",
		"AWS_MULTIPLE_SERVICES_OPERATIONAL_ISSUE",
		"AWS_LAMBDA_API_ISSUE",
	}
)

// genEventArn draws a detail.eventArn value. It covers realistic AWS Health
// ARNs, values with zero, one, and many "/" separators, and values chosen so
// the assembled tag string "arn:<value>" lands below, at, and above the
// 200-character limit.
func genEventArn() *rapid.Generator[string] {
	return rapid.OneOf(
		genRealisticEventArn(),
		genArnWithSlashCount(0),
		genArnWithSlashCount(1),
		genArnWithSlashCount(2),
		genArnWithSlashCount(7),
		genArnAroundTagLimit(),
		genLongValue(),
		genFieldValue(),
	)
}

// genRealisticEventArn draws ARNs shaped like the ones AWS Health emits.
func genRealisticEventArn() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		region := rapid.SampledFrom(arnRegions).Draw(t, "arnRegion")
		service := rapid.SampledFrom(arnServices).Draw(t, "arnService")
		code := rapid.SampledFrom(arnCodes).Draw(t, "arnCode")
		suffix := rapid.StringOfN(rapid.RuneFrom([]rune("0123456789abcdef-")), 8, 36, 36).
			Draw(t, "arnSuffix")
		return "arn:aws:health:" + region + "::event/" + service + "/" + code + "/" + code + "_" + suffix
	})
}

// genArnWithSlashCount draws an ARN-like value containing exactly n "/"
// separators, so splitting or trimming on "/" cannot pass unnoticed.
func genArnWithSlashCount(n int) *rapid.Generator[string] {
	segment := rapid.StringOfN(rapid.RuneFrom([]rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ_0123456789")), 1, 12, 12)
	return rapid.Custom(func(t *rapid.T) string {
		// n+1 slash-free segments joined by "/" carry exactly n separators.
		segments := rapid.SliceOfN(segment, n+1, n+1).Draw(t, "arnSegments")
		return "arn:aws:health:af-south-1::event" + strings.Join(segments, "/")
	})
}

// genArnAroundTagLimit draws ARNs whose assembled "arn:<value>" tag string
// falls just below, exactly at, and just above maxTagLength characters.
func genArnAroundTagLimit() *rapid.Generator[string] {
	prefixLen := len(tagKeyARN) + 1 // "arn:"
	return rapid.Custom(func(t *rapid.T) string {
		// tag length spans [maxTagLength-4, maxTagLength+4].
		tagLen := rapid.IntRange(maxTagLength-4, maxTagLength+4).Draw(t, "arnTagLength")
		return strings.Repeat("x", tagLen-prefixLen)
	})
}

// --- Status codes -----------------------------------------------------------

// nonClosedStatusSeeds are the specific non-closed values the design calls out.
var nonClosedStatusSeeds = []string{"", "open", "upcoming", "Closed", "CLOSED", " closed", "closed ", "clos ed"}

// genStatusCode draws a detail.statusCode value: either the exact lowercase
// "closed" that gates submission, or any of the non-closed values covered by
// genNonClosedStatusCode.
func genStatusCode() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.Just(statusCodeClosed),
		genNonClosedStatusCode(),
	)
}

// genNonClosedStatusCode draws arbitrary status values filtered to exclude the
// exact lowercase "closed", seeded with "", "open", "upcoming", "Closed",
// "CLOSED", and " closed". Property 7 draws from this variant.
func genNonClosedStatusCode() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.SampledFrom(nonClosedStatusSeeds),
		rapid.SampledFrom(unicodeValues),
		rapid.String(),
	).Filter(func(v string) bool { return v != statusCodeClosed })
}

// --- Instants ---------------------------------------------------------------

// instantPair is a pair of UTC instants at whole-second granularity together
// with their rendering under healthEventTimeFormat and the signed second delta
// the Forwarder must report for them.
type instantPair struct {
	Start        time.Time
	End          time.Time
	StartRaw     string
	EndRaw       string
	DeltaSeconds float64
}

// newInstantPair renders both instants with the AWS Health layout and records
// the signed delta, in seconds, of end minus start.
func newInstantPair(start, end time.Time) instantPair {
	start = start.UTC().Truncate(time.Second)
	end = end.UTC().Truncate(time.Second)
	return instantPair{
		Start:        start,
		End:          end,
		StartRaw:     start.Format(healthEventTimeFormat),
		EndRaw:       end.Format(healthEventTimeFormat),
		DeltaSeconds: end.Sub(start).Seconds(),
	}
}

// yearBoundaryInstants sit either side of a year rollover, so a pair spanning
// New Year is reachable.
func yearBoundaryInstants() []time.Time {
	var instants []time.Time
	for _, year := range []int{1999, 2000, 2015, 2024, 2025, 2099} {
		instants = append(instants,
			time.Date(year, time.December, 31, 23, 59, 59, 0, time.UTC),
			time.Date(year, time.December, 31, 23, 59, 0, 0, time.UTC),
			time.Date(year+1, time.January, 1, 0, 0, 0, 0, time.UTC),
		)
	}
	return instants
}

// genInstant draws a UTC instant truncated to whole seconds, biased towards
// year boundaries and a broad span of ordinary timestamps.
func genInstant() *rapid.Generator[time.Time] {
	minUnix := time.Date(1971, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	maxUnix := time.Date(2098, time.December, 31, 23, 59, 59, 0, time.UTC).Unix()
	return rapid.OneOf(
		rapid.Map(rapid.Int64Range(minUnix, maxUnix), func(sec int64) time.Time {
			return time.Unix(sec, 0).UTC()
		}),
		rapid.SampledFrom(yearBoundaryInstants()),
	)
}

// genGapSeconds draws a signed second offset covering equal instants,
// sub-day gaps, multi-day gaps, and inverted (negative) gaps.
func genGapSeconds() *rapid.Generator[int64] {
	const day = int64(86400)
	return rapid.OneOf(
		rapid.Just(int64(0)),
		rapid.Int64Range(1, day-1),
		rapid.Int64Range(day, 45*day),
		rapid.Int64Range(-45*day, -1),
	)
}

// genInstantPair draws ordered, equal, and inverted pairs of UTC instants,
// including multi-day gaps and pairs spanning a year boundary. Both instants
// are truncated to whole seconds and rendered with healthEventTimeFormat, so
// the raw values always parse and always round-trip.
func genInstantPair() *rapid.Generator[instantPair] {
	return rapid.Custom(func(t *rapid.T) instantPair {
		start := genInstant().Draw(t, "startInstant")
		gap := genGapSeconds().Draw(t, "gapSeconds")
		return newInstantPair(start, start.Add(time.Duration(gap)*time.Second))
	})
}

// genInvertedInstantPair draws pairs whose end instant strictly precedes the
// start instant. Property 4 draws from this variant.
func genInvertedInstantPair() *rapid.Generator[instantPair] {
	return rapid.Custom(func(t *rapid.T) instantPair {
		end := genInstant().Draw(t, "endInstant")
		gap := rapid.Int64Range(1, 45*86400).Draw(t, "invertedGapSeconds")
		return newInstantPair(end.Add(time.Duration(gap)*time.Second), end)
	}).Filter(func(p instantPair) bool { return p.DeltaSeconds < 0 })
}

// --- Bad timestamps ---------------------------------------------------------

// badTimestampSeeds are timestamp values the AWS Health layout rejects: the
// empty string, ISO-8601 and RFC3339 renderings, the layout with a component
// missing, non-date noise, and valid-shaped but invalid dates.
var badTimestampSeeds = []string{
	"",
	"2025-01-06T10:00:00Z",
	"2025-01-06T10:00:00+02:00",
	"2025-01-06 10:00:00",
	"20250106T100000Z",
	"Mon, 6 Jan 2025 GMT",
	"Mon, 6 Jan 2025 10:00:00",
	"6 Jan 2025 10:00:00 GMT",
	"Mon, Jan 2025 10:00:00 GMT",
	"Mon, 6 Jan 10:00:00 GMT",
	"not a timestamp",
	"0",
	"null",
	"   ",
	"Mon, 32 Jan 2025 10:00:00 GMT",
	"Mon, 30 Feb 2025 10:00:00 GMT",
	"Mon, 6 Xxx 2025 10:00:00 GMT",
	"Mon, 6 Jan 2025 25:00:00 GMT",
	"Mon, 6 Jan 2025 10:61:00 GMT",
	"Mon, 6 Jan 2025 10:00:00 UTC",
}

// genBadTimestamp draws a startTime or endTime value that the AWS Health
// layout rejects, or the empty string. Every draw is filtered so a value that
// happens to parse can never reach a property.
func genBadTimestamp() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.SampledFrom(badTimestampSeeds),
		rapid.Map(genInstant(), func(at time.Time) string { return at.Format(time.RFC3339) }),
		rapid.Map(genInstant(), func(at time.Time) string { return at.Format(time.RFC1123Z) }),
		rapid.String(),
		rapid.SampledFrom(unicodeValues),
	).Filter(func(v string) bool {
		if v == "" {
			return true
		}
		_, err := time.Parse(healthEventTimeFormat, v)
		return err != nil
	})
}

// --- Malformed detail payloads ----------------------------------------------

// malformedDetailSeeds are detail payloads encoding/json cannot unmarshal into
// a HealthEventDetail: truncated objects, JSON arrays, JSON scalars, wrong
// types for string fields, and empty bytes.
var malformedDetailSeeds = []string{
	"",
	"{",
	"{\"eventArn\":",
	"{\"eventArn\":\"arn:aws:health:af-south-1::event/EC2/CODE/ID\"",
	"{\"statusCode\":\"closed\",",
	"[]",
	"[{\"eventArn\":\"arn:aws\"}]",
	"[\"closed\"]",
	"123",
	"12.5",
	"true",
	"false",
	"\"closed\"",
	"{\"eventArn\":123}",
	"{\"service\":[\"EC2\"]}",
	"{\"statusCode\":true}",
	"{\"startTime\":{\"at\":\"Mon, 6 Jan 2025 10:00:00 GMT\"}}",
	"{\"endTime\":null,\"eventRegion\":42}",
	"not json at all",
}

// genMalformedDetail draws detail bytes that json.Unmarshal rejects for a
// HealthEventDetail target: truncated JSON, JSON arrays, JSON scalars, wrong
// types for string fields, empty bytes, and non-UTF-8 bytes. Draws are
// filtered against a real unmarshal, so a payload that happens to decode can
// never reach a property.
func genMalformedDetail() *rapid.Generator[[]byte] {
	return rapid.OneOf(
		rapid.Map(rapid.SampledFrom(malformedDetailSeeds), func(s string) []byte { return []byte(s) }),
		genNonUTF8Bytes(),
		rapid.SliceOf(rapid.Byte()),
	).Filter(func(payload []byte) bool {
		var detail HealthEventDetail
		return json.Unmarshal(payload, &detail) != nil
	})
}

// genNonUTF8Bytes draws byte sequences that are not valid UTF-8 and not valid
// JSON.
func genNonUTF8Bytes() *rapid.Generator[[]byte] {
	return rapid.SliceOfN(rapid.ByteRange(0x80, 0xff), 1, 16)
}

// --- Detail assembly --------------------------------------------------------

// drawDetail assembles a HealthEventDetail from generated parts: the caller
// supplies the status code and the two raw timestamp values, and the four
// verbatim tag source fields are drawn from genEventArn and genFieldValue.
// The label prefix keeps draw labels unique when a property assembles more
// than one detail.
func drawDetail(t *rapid.T, labelPrefix, statusCode, startRaw, endRaw string) HealthEventDetail {
	return HealthEventDetail{
		EventArn:      genEventArn().Draw(t, labelPrefix+"eventArn"),
		Service:       genFieldValue().Draw(t, labelPrefix+"service"),
		EventTypeCode: genFieldValue().Draw(t, labelPrefix+"eventTypeCode"),
		StatusCode:    statusCode,
		StartTime:     startRaw,
		EndTime:       endRaw,
		EventRegion:   genFieldValue().Draw(t, labelPrefix+"eventRegion"),
	}
}

// --- Smoke test -------------------------------------------------------------

// TestGeneratorsSatisfyTheirCoverageObligations draws from every generator and
// checks the invariant each one promises to the property tests that use it.
func TestGeneratorsSatisfyTheirCoverageObligations(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		_ = genFieldValue().Draw(t, "fieldValue")
		_ = genAccountID().Draw(t, "accountID")
		_ = genEventArn().Draw(t, "eventArn")
		_ = genStatusCode().Draw(t, "statusCode")

		if status := genNonClosedStatusCode().Draw(t, "nonClosedStatusCode"); status == statusCodeClosed {
			t.Fatalf("genNonClosedStatusCode drew the closed status %q", status)
		}

		pair := genInstantPair().Draw(t, "instantPair")
		start, err := time.Parse(healthEventTimeFormat, pair.StartRaw)
		if err != nil {
			t.Fatalf("genInstantPair produced an unparseable startTime %q: %v", pair.StartRaw, err)
		}
		end, err := time.Parse(healthEventTimeFormat, pair.EndRaw)
		if err != nil {
			t.Fatalf("genInstantPair produced an unparseable endTime %q: %v", pair.EndRaw, err)
		}
		if got := end.Sub(start).Seconds(); got != pair.DeltaSeconds {
			t.Fatalf("genInstantPair delta = %v, want %v", pair.DeltaSeconds, got)
		}

		if inverted := genInvertedInstantPair().Draw(t, "invertedInstantPair"); inverted.DeltaSeconds >= 0 {
			t.Fatalf("genInvertedInstantPair delta = %v, want a negative value", inverted.DeltaSeconds)
		}

		if bad := genBadTimestamp().Draw(t, "badTimestamp"); bad != "" {
			if _, err := time.Parse(healthEventTimeFormat, bad); err == nil {
				t.Fatalf("genBadTimestamp drew %q, which the layout accepts", bad)
			}
		}

		payload := genMalformedDetail().Draw(t, "malformedDetail")
		var detail HealthEventDetail
		if err := json.Unmarshal(payload, &detail); err == nil {
			t.Fatalf("genMalformedDetail drew %q, which unmarshals cleanly", payload)
		}

		assembled := drawDetail(t, "assembled/", statusCodeClosed, pair.StartRaw, pair.EndRaw)
		if assembled.StatusCode != statusCodeClosed || assembled.StartTime != pair.StartRaw {
			t.Fatalf("drawDetail did not carry the supplied parts: %+v", assembled)
		}
	})
}
