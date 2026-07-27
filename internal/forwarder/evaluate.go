package forwarder

import "errors"

// statusCodeClosed is the AWS Health event statusCode value indicating a
// resolved/closed issue.
const statusCodeClosed = "closed"

// metricNameDuration is the only Datadog custom metric name the Forwarder emits.
const metricNameDuration = "aws.health.events.duration"

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
