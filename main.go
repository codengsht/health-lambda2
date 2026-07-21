// Package main is the entry point for the aws-health-event-forwarder AWS Lambda
// function. It receives AWS Health events (forwarded via EventBridge) and
// emits corresponding metrics to Datadog.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	ddlambda "github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// healthEventTimeFormat is the RFC2822-style format AWS Health uses for time fields.
const healthEventTimeFormat = "Mon, 2 Jan 2006 15:04:05 GMT"

// statusCodeClosed is the AWS Health event statusCode value indicating a
// resolved/closed issue.
const statusCodeClosed = "closed"

// HealthEventDetail maps the fields from an AWS Health event detail payload.
type HealthEventDetail struct {
	EventArn          string `json:"eventArn"`
	Service           string `json:"service"`
	EventTypeCode     string `json:"eventTypeCode"`
	EventTypeCategory string `json:"eventTypeCategory"`
	EventScopeCode    string `json:"eventScopeCode"`
	StatusCode        string `json:"statusCode"`
	StartTime         string `json:"startTime"`
	EndTime           string `json:"endTime"`
	EventRegion       string `json:"eventRegion"`
	AccountID         string `json:"account"`
	CommunicationID   string `json:"communicationId"`
	EventDescription  []struct {
		Language          string `json:"language"`
		LatestDescription string `json:"latestDescription"`
	} `json:"eventDescription"`
}

// eventArnTagValue trims an AWS Health eventArn down to the event-type-code
// and unique event_id portion (everything after the second "/"), keeping the
// tag value well under Datadog's 200-character tag limit. For example:
//
//	arn:aws:health:af-south-1::event/EC2/AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae-af1f-54e6-a526-d0179ed6d68f
//
// becomes:
//
//	AWS_EC2_OPERATIONAL_ISSUE/AWS_EC2_OPERATIONAL_ISSUE_7f35c8ae-af1f-54e6-a526-d0179ed6d68f
func eventArnTagValue(eventArn string) string {
	parts := strings.SplitN(eventArn, "/", 3)
	if len(parts) < 3 {
		return eventArn
	}
	return parts[2]
}

func handleRequest(ctx context.Context, event events.CloudWatchEvent) error {
	log.Printf("Received AWS Health event: source=%s detail-type=%s region=%s",
		event.Source, event.DetailType, event.Region)

	var detail HealthEventDetail
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return fmt.Errorf("failed to unmarshal health event detail: %w", err)
	}

	// event_arn is truncated to everything after the second "/" (event type
	// code + unique event id) to stay well under Datadog's 200-character tag
	// value limit while still letting a metric be correlated back to its
	// originating event.
	tags := []string{
		"service:" + detail.Service,
		"event_type_code:" + detail.EventTypeCode,
		"event_type_category:" + detail.EventTypeCategory,
		"status:" + detail.StatusCode,
		"region:" + detail.EventRegion,
		"event_arn:" + eventArnTagValue(detail.EventArn),
		"communication_id:" + detail.CommunicationID,
	}

	// Send a count metric for every event received (open or closed).
	ddlambda.Metric("aws.health.issue.received", 1, tags...)

	// Emit an alert status gauge: 1 = active issue, 0 = resolved.
	// This allows Datadog monitors to alert when a specific service/event is open
	// and auto-recover when it closes, without requiring stateful correlation.
	alertStatus := 1.0
	if detail.StatusCode == statusCodeClosed {
		alertStatus = 0.0
	}
	ddlambda.Metric("aws.health.issue.alert_status", alertStatus, tags...)
	log.Printf("Alert status: %.0f (statusCode=%s)", alertStatus, detail.StatusCode)

	// On resolved events, compute and emit outage duration in seconds.
	if detail.StatusCode == statusCodeClosed && detail.StartTime != "" && detail.EndTime != "" {
		startTime, err := time.Parse(healthEventTimeFormat, detail.StartTime)
		if err != nil {
			log.Printf("Warning: could not parse startTime %q: %v", detail.StartTime, err)
		}

		endTime, err := time.Parse(healthEventTimeFormat, detail.EndTime)
		if err != nil {
			log.Printf("Warning: could not parse endTime %q: %v", detail.EndTime, err)
		}

		if !startTime.IsZero() && !endTime.IsZero() {
			durationSeconds := endTime.Sub(startTime).Seconds()
			// Distribution preserves all samples within a rollup window, enabling
			// accurate sum/avg/p99/count queries even when multiple events close
			// simultaneously — unlike a gauge which keeps only the last value.
			ddlambda.Distribution("aws.health.issue.duration_seconds", durationSeconds, tags...)
			log.Printf("Outage duration: %.0f seconds (%.2f hours)", durationSeconds, durationSeconds/3600)
		}
	}

	log.Printf("Forwarded health event to Datadog: service=%s type=%s status=%s",
		detail.Service, detail.EventTypeCode, detail.StatusCode)

	return nil
}

func main() {
	lambda.Start(ddlambda.WrapFunction(handleRequest, nil))
}
