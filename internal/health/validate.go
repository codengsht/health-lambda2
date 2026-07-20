package health

import (
	"strings"
	"time"
)

func validateRequired(detail requiredDetail) []string {
	fields := []struct {
		name  string
		value string
	}{
		{name: "eventArn", value: detail.EventARN},
		{name: "communicationId", value: detail.CommunicationID},
		{name: "service", value: detail.Service},
		{name: "eventTypeCode", value: detail.EventTypeCode},
		{name: "eventTypeCategory", value: detail.EventTypeCategory},
		{name: "eventScopeCode", value: detail.EventScopeCode},
		{name: "statusCode", value: detail.StatusCode},
		{name: "startTime", value: detail.StartTime},
		{name: "eventRegion", value: detail.EventRegion},
	}

	invalid := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			invalid = append(invalid, field.name)
			continue
		}
		if field.name == "startTime" {
			if _, err := parseHealthEventTime(field.value); err != nil {
				invalid = append(invalid, field.name)
			}
		}
	}

	return invalid
}

func parseHealthEventTime(value string) (time.Time, error) {
	return time.Parse(healthEventTimeFormat, value)
}
