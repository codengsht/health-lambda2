package health

import "strings"

func buildSuccessRecord(
	envelope eventBridgeEnvelope,
	required requiredDetail,
	optional optionalDetail,
) successRecord {
	duration := calculateDuration(required, optional.EndTime)

	return successRecord{
		RecordType:  successRecordType,
		Message:     "AWS Health event processed",
		EventBridge: eventBridgeMetadata(envelope),
		AWSHealth: awsHealthRecord{
			EventARN:                  required.EventARN,
			CommunicationID:           required.CommunicationID,
			Service:                   required.Service,
			EventTypeCode:             required.EventTypeCode,
			EventTypeCategory:         required.EventTypeCategory,
			EventScopeCode:            required.EventScopeCode,
			StatusCode:                required.StatusCode,
			StartTime:                 required.StartTime,
			EventRegion:               required.EventRegion,
			EndTime:                   optional.EndTime,
			LastUpdatedTime:           optional.LastUpdatedTime,
			LatestDescription:         optional.LatestDescription,
			AffectedEntities:          optional.AffectedEntities,
			BackupEvent:               optional.BackupEvent,
			Page:                      optional.Page,
			TotalPages:                optional.TotalPages,
			AffectedAccount:           optional.AffectedAccount,
			Actionability:             optional.Actionability,
			Personas:                  optional.Personas,
			DurationSeconds:           duration.Seconds,
			DurationCalculationStatus: duration.Status,
		},
	}
}

func buildFailureRecord(
	stage string,
	message string,
	envelope *eventBridgeEnvelope,
	invalidFields []string,
) failureRecord {
	record := failureRecord{
		RecordType:             failureRecordType,
		FailureStage:           stage,
		Message:                message,
		MissingOrInvalidFields: invalidFields,
	}
	if envelope != nil {
		metadata := eventBridgeMetadata(*envelope)
		record.EventBridge = &metadata
	}
	return record
}

func eventBridgeMetadata(envelope eventBridgeEnvelope) eventBridgeRecord {
	return eventBridgeRecord{
		ID:               nonBlank(envelope.ID),
		Source:           nonBlank(envelope.Source),
		DetailType:       nonBlank(envelope.DetailType),
		ReceivingAccount: nonBlank(envelope.Account),
		DeliveryRegion:   nonBlank(envelope.Region),
		NotificationTime: nonBlank(envelope.Time),
	}
}

func nonBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}
