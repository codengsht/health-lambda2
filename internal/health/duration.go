package health

func calculateDuration(detail requiredDetail, endTime *string) durationResult {
	switch detail.StatusCode {
	case "open", "upcoming":
		return durationResult{Status: "not_final"}
	case "closed":
		if endTime == nil {
			return durationResult{Status: "missing_end_time"}
		}

		end, err := parseHealthEventTime(*endTime)
		if err != nil {
			return durationResult{Status: "invalid_end_time"}
		}
		start, err := parseHealthEventTime(detail.StartTime)
		if err != nil {
			// Required-field validation runs before duration calculation. Retain a
			// defensive status for direct unit use rather than producing a value.
			return durationResult{Status: "invalid_start_time"}
		}
		if end.Before(start) {
			return durationResult{Status: "negative_duration"}
		}

		seconds := int64(end.Sub(start).Seconds())
		return durationResult{Seconds: &seconds, Status: "calculated"}
	default:
		return durationResult{Status: "unsupported_status"}
	}
}
