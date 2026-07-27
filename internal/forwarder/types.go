package forwarder

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
