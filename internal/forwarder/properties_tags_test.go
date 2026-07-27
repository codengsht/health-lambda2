package forwarder

// Property tests over the tag set the Forwarder attaches to every
// aws.health.events.duration sample.

import (
	"flag"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// tagsPropertyMinChecks is the explicit iteration floor for the properties in
// this file, so the minimum is visible in the source rather than implied by
// rapid's default check count.
const tagsPropertyMinChecks = 100

// enforceTagsPropertyMinChecks raises rapid's check count to
// tagsPropertyMinChecks when the ambient setting is lower, and never lowers a
// higher setting supplied on the command line.
func enforceTagsPropertyMinChecks(t *testing.T) {
	t.Helper()

	f := flag.Lookup("rapid.checks")
	if f == nil {
		t.Fatalf("rapid.checks flag is not registered; cannot assert an iteration floor")
	}

	getter, ok := f.Value.(flag.Getter)
	if !ok {
		t.Fatalf("rapid.checks flag value %T does not expose its value", f.Value)
	}

	current, ok := getter.Get().(int)
	if !ok {
		t.Fatalf("rapid.checks flag value %T is not an int", getter.Get())
	}

	if current >= tagsPropertyMinChecks {
		return
	}

	previous := strconv.Itoa(current)
	if err := f.Value.Set(strconv.Itoa(tagsPropertyMinChecks)); err != nil {
		t.Fatalf("setting rapid.checks to %d: %v", tagsPropertyMinChecks, err)
	}
	t.Cleanup(func() { _ = f.Value.Set(previous) })
}

// expectedTagKeys is the exact tag key set, in the fixed order metricTags
// returns, paired with the source value each key must carry byte-for-byte.
var expectedTagKeys = []string{
	tagKeyReceivingAccount,
	tagKeyARN,
	tagKeyAWSService,
	tagKeyAffectedRegion,
	tagKeyEventTypeCode,
}

// forbiddenTagKeys are the keys the Forwarder must never emit: the previous
// ARN key it replaced, and the keys that belonged to the removed metrics.
var forbiddenTagKeys = []string{
	"event_arn",
	"event_scope_code",
	"status_code",
	"event_type_category",
}

// Feature: aws-health-event-telemetry, Property 2: The tag set is exactly five verbatim key:value pairs carrying the Full_ARN under arn
//
// For any account id and any Health_Event_Detail — including empty,
// whitespace-only, mixed-case, unicode, and over-long field values — the tags
// built for a sample are exactly five strings, one per key in
// {receiving_account, arn, aws_service, affected_region, event_type_code} with
// no key repeated and no sixth key; each tag equals its key, a colon, and the
// corresponding source value copied byte-for-byte with no truncation,
// splitting, case change, whitespace trimming, or encoding; the ARN appears
// under the arn key and under no other key; and no tag key is event_arn,
// event_scope_code, status_code, or event_type_category.
//
// Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.6, 2.8, 2.9
func TestPropertyTagSetIsExactlyFiveVerbatimPairs(t *testing.T) {
	enforceTagsPropertyMinChecks(t)

	rapid.Check(t, func(t *rapid.T) {
		accountID := genAccountID().Draw(t, "accountID")
		pair := genInstantPair().Draw(t, "instantPair")
		detail := drawDetail(t, "", statusCodeClosed, pair.StartRaw, pair.EndRaw)

		tags := metricTags(accountID, detail)

		// Exactly five tags, and the tag set carried by an actual sample is
		// the very same set (Requirement 2.1).
		if len(tags) != len(expectedTagKeys) {
			t.Fatalf("metricTags returned %d tags, want %d: %q", len(tags), len(expectedTagKeys), tags)
		}

		result, err := evaluate(accountID, detail)
		if err != nil {
			t.Fatalf("evaluate returned an error for a closed event with parseable timestamps: %v", err)
		}
		if result.Sample == nil {
			t.Fatalf("evaluate returned no sample for a closed event with parseable timestamps")
		}
		if len(result.Sample.Tags) != len(tags) {
			t.Fatalf("sample carries %d tags, want %d: %q", len(result.Sample.Tags), len(tags), result.Sample.Tags)
		}
		for i, tag := range tags {
			if result.Sample.Tags[i] != tag {
				t.Fatalf("sample tag %d = %q, want %q", i, result.Sample.Tags[i], tag)
			}
		}

		// Every tag is key:value, and the value is the source field copied
		// byte-for-byte (Requirements 2.2, 2.3, 2.6, 2.9).
		wantValues := map[string]string{
			tagKeyReceivingAccount: accountID,
			tagKeyARN:              detail.EventArn,
			tagKeyAWSService:       detail.Service,
			tagKeyAffectedRegion:   detail.EventRegion,
			tagKeyEventTypeCode:    detail.EventTypeCode,
		}

		seen := make(map[string]int, len(tags))
		for _, tag := range tags {
			key, value, found := strings.Cut(tag, ":")
			if !found {
				t.Fatalf("tag %q is not formatted as key:value", tag)
			}

			want, known := wantValues[key]
			if !known {
				t.Fatalf("tag %q carries key %q, which is not in the expected key set %q", tag, key, expectedTagKeys)
			}
			if value != want {
				t.Fatalf("tag %q value = %q, want the source value %q byte-for-byte", key, value, want)
			}
			if tag != key+":"+want {
				t.Fatalf("tag = %q, want %q", tag, key+":"+want)
			}

			seen[key]++
		}

		// No key repeats and no sixth key (Requirement 2.1).
		for _, key := range expectedTagKeys {
			switch seen[key] {
			case 1:
			case 0:
				t.Fatalf("tag key %q is missing from %q", key, tags)
			default:
				t.Fatalf("tag key %q appears %d times in %q, want exactly once", key, seen[key], tags)
			}
		}
		if len(seen) != len(expectedTagKeys) {
			t.Fatalf("tags carry %d distinct keys, want %d: %q", len(seen), len(expectedTagKeys), tags)
		}

		// The ARN identity is carried under the arn key only, with no alias
		// key repeating it (Requirements 2.4, 2.8).
		arnTags := 0
		for _, tag := range tags {
			key, value, _ := strings.Cut(tag, ":")
			if key == tagKeyARN {
				arnTags++
				if value != detail.EventArn {
					t.Fatalf("arn tag value = %q, want the Full_ARN %q", value, detail.EventArn)
				}
				continue
			}
			// Another key may only repeat the ARN string when its own source
			// field happens to hold that same value.
			if value == detail.EventArn && wantValues[key] != detail.EventArn {
				t.Fatalf("tag key %q aliases the ARN value %q", key, detail.EventArn)
			}
		}
		if arnTags != 1 {
			t.Fatalf("found %d %q tags, want exactly 1: %q", arnTags, tagKeyARN, tags)
		}

		// None of the retired or removed-metric keys may appear
		// (Requirement 2.4).
		for _, forbidden := range forbiddenTagKeys {
			if _, present := seen[forbidden]; present {
				t.Fatalf("tags carry the forbidden key %q: %q", forbidden, tags)
			}
			for _, tag := range tags {
				if key, _, _ := strings.Cut(tag, ":"); key == forbidden {
					t.Fatalf("tag %q carries the forbidden key %q", tag, forbidden)
				}
			}
		}
	})
}

// Feature: aws-health-event-telemetry, Property 9: An over-limit ARN tag is emitted untruncated and reported
//
// For any eventArn value, the emitted arn tag value equals the input with no
// shortening, and a tag-length notice recording the tag key and its character
// count is produced exactly when the assembled tag string "arn:<eventArn>"
// exceeds 200 characters.
//
// Validates: Requirements 2.5, 2.10
func TestPropertyOverLimitArnTagIsEmittedUntruncatedAndReported(t *testing.T) {
	enforceTagsPropertyMinChecks(t)

	rapid.Check(t, func(t *rapid.T) {
		accountID := genAccountID().Draw(t, "accountID")
		pair := genInstantPair().Draw(t, "instantPair")
		detail := drawDetail(t, "", statusCodeClosed, pair.StartRaw, pair.EndRaw)

		// Draw the ARN under test from the boundary-heavy generators, so tag
		// strings just below, exactly at, and just above the limit are all
		// reachable.
		detail.EventArn = rapid.OneOf(
			genArnAroundTagLimit(),
			genEventArn(),
			genLongValue(),
		).Draw(t, "arnUnderTest")

		wantTag := tagKeyARN + ":" + detail.EventArn
		overLimit := len(wantTag) > maxTagLength

		result, err := evaluate(accountID, detail)
		if err != nil {
			t.Fatalf("evaluate returned an error for a closed event with parseable timestamps: %v", err)
		}
		if result.Sample == nil {
			t.Fatalf("evaluate returned no sample for a closed event with parseable timestamps")
		}

		// The arn tag carries the input with no shortening, whatever its
		// length (Requirements 2.5, 2.10).
		arnTags := 0
		for _, tag := range result.Sample.Tags {
			key, value, found := strings.Cut(tag, ":")
			if !found || key != tagKeyARN {
				continue
			}
			arnTags++
			if value != detail.EventArn {
				t.Fatalf("arn tag value = %q (%d chars), want the input %q (%d chars) unshortened",
					value, len(value), detail.EventArn, len(detail.EventArn))
			}
			if tag != wantTag {
				t.Fatalf("arn tag = %q, want %q", tag, wantTag)
			}
			if len(tag) != len(wantTag) {
				t.Fatalf("arn tag length = %d, want %d", len(tag), len(wantTag))
			}
		}
		if arnTags != 1 {
			t.Fatalf("found %d %q tags, want exactly 1: %q", arnTags, tagKeyARN, result.Sample.Tags)
		}

		// A tag-length notice for the arn key appears exactly when the
		// assembled tag string exceeds the limit, and records the key and the
		// character count (Requirement 2.10).
		arnNotices := 0
		for _, n := range result.Notices {
			if n.Kind != noticeTagTooLong || n.Fields["tagKey"] != tagKeyARN {
				continue
			}
			arnNotices++
			if got, want := n.Fields["length"], strconv.Itoa(len(wantTag)); got != want {
				t.Fatalf("arn tag-length notice length = %q, want %q", got, want)
			}
		}

		switch {
		case overLimit && arnNotices != 1:
			t.Fatalf("arn tag %q is %d chars (> %d) but produced %d tag-length notices, want exactly 1",
				wantTag, len(wantTag), maxTagLength, arnNotices)
		case !overLimit && arnNotices != 0:
			t.Fatalf("arn tag %q is %d chars (<= %d) but produced %d tag-length notices, want none",
				wantTag, len(wantTag), maxTagLength, arnNotices)
		}
	})
}
