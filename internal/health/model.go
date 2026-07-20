package health

import "encoding/json"

const (
	healthEventTimeFormat      = "Mon, 2 Jan 2006 15:04:05 MST"
	notificationActivityMetric = "aws.health.issue.received"
	successRecordType          = "aws_health_public_issue"
	failureRecordType          = "aws_health_processing_failure"
	failureStageEnvelopeParse  = "envelope_parse"
	failureStageDetailParse    = "detail_parse"
	failureStageValidation     = "validation"
)

type eventBridgeEnvelope struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Account    string          `json:"account"`
	Time       string          `json:"time"`
	Region     string          `json:"region"`
	Detail     json.RawMessage `json:"detail"`
}

// requiredDetail is deliberately separate from optionalDetailRaw. Unmarshalling
// into this type fails closed when a required field has an incompatible JSON
// type, while malformed optional fields can be discarded independently.
type requiredDetail struct {
	EventARN          string `json:"eventArn"`
	CommunicationID   string `json:"communicationId"`
	Service           string `json:"service"`
	EventTypeCode     string `json:"eventTypeCode"`
	EventTypeCategory string `json:"eventTypeCategory"`
	EventScopeCode    string `json:"eventScopeCode"`
	StatusCode        string `json:"statusCode"`
	StartTime         string `json:"startTime"`
	EventRegion       string `json:"eventRegion"`
}

type optionalDetailRaw struct {
	EndTime          json.RawMessage `json:"endTime"`
	LastUpdatedTime  json.RawMessage `json:"lastUpdatedTime"`
	EventDescription json.RawMessage `json:"eventDescription"`
	AffectedEntities json.RawMessage `json:"affectedEntities"`
	BackupEvent      json.RawMessage `json:"backupEvent"`
	Page             json.RawMessage `json:"page"`
	TotalPages       json.RawMessage `json:"totalPages"`
	AffectedAccount  json.RawMessage `json:"affectedAccount"`
	Actionability    json.RawMessage `json:"actionability"`
	Personas         json.RawMessage `json:"personas"`
}

type optionalDetail struct {
	EndTime           *string
	LastUpdatedTime   *string
	LatestDescription *string
	AffectedEntities  json.RawMessage
	BackupEvent       *string
	Page              *string
	TotalPages        *string
	AffectedAccount   *string
	Actionability     *string
	Personas          []string
}

type eventBridgeRecord struct {
	ID               string `json:"id,omitempty"`
	Source           string `json:"source,omitempty"`
	DetailType       string `json:"detail_type,omitempty"`
	ReceivingAccount string `json:"receiving_account,omitempty"`
	DeliveryRegion   string `json:"delivery_region,omitempty"`
	NotificationTime string `json:"notification_time,omitempty"`
}

type awsHealthRecord struct {
	EventARN          string `json:"event_arn"`
	CommunicationID   string `json:"communication_id"`
	Service           string `json:"service"`
	EventTypeCode     string `json:"event_type_code"`
	EventTypeCategory string `json:"event_type_category"`
	EventScopeCode    string `json:"event_scope_code"`
	StatusCode        string `json:"status_code"`
	StartTime         string `json:"start_time"`
	EventRegion       string `json:"event_region"`

	EndTime           *string         `json:"end_time,omitempty"`
	LastUpdatedTime   *string         `json:"last_updated_time,omitempty"`
	LatestDescription *string         `json:"latest_description,omitempty"`
	AffectedEntities  json.RawMessage `json:"affected_entities,omitempty"`
	BackupEvent       *string         `json:"backup_event,omitempty"`
	Page              *string         `json:"page,omitempty"`
	TotalPages        *string         `json:"total_pages,omitempty"`
	AffectedAccount   *string         `json:"affected_account,omitempty"`
	Actionability     *string         `json:"actionability,omitempty"`
	Personas          []string        `json:"personas,omitempty"`

	DurationSeconds           *int64 `json:"duration_seconds,omitempty"`
	DurationCalculationStatus string `json:"duration_calculation_status"`
}

type successRecord struct {
	RecordType  string            `json:"record_type"`
	Message     string            `json:"message"`
	EventBridge eventBridgeRecord `json:"eventbridge"`
	AWSHealth   awsHealthRecord   `json:"aws_health"`
}

type failureRecord struct {
	RecordType             string             `json:"record_type"`
	FailureStage           string             `json:"failure_stage"`
	Message                string             `json:"message"`
	MissingOrInvalidFields []string           `json:"missing_or_invalid_fields,omitempty"`
	EventBridge            *eventBridgeRecord `json:"eventbridge,omitempty"`
}

type durationResult struct {
	Seconds *int64
	Status  string
}
