package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	ddlambda "github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2"
	"github.com/aws/aws-lambda-go/events"
)

// submitMetric is the single submission seam of the Forwarder. Production code
// never reassigns it and it holds no per-invocation data; tests replace it with
// a recording fake. ddlambda.Metric submits a Datadog distribution and returns
// nothing, so a submission failure is not observable here (see README).
var submitMetric = ddlambda.Metric

// Handle is the impure edge of the Forwarder: it decodes the detail, delegates
// every decision to evaluate, writes the log lines the evaluation asks for, and
// performs at most one metric submission. It contains no arithmetic, no status
// comparison, and no tag construction, and it never serializes or forwards the
// Event_Payload — a returned error fails the invocation, which is what routes
// the unmodified payload to the DLQ.
func Handle(ctx context.Context, event events.CloudWatchEvent) error {
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
