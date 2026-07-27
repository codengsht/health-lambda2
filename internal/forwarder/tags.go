package forwarder

import (
	"strconv"
	"strings"
)

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
