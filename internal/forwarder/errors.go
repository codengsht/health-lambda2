package forwarder

import "fmt"

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
