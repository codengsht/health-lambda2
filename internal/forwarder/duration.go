package forwarder

import (
	"fmt"
	"time"
)

// healthEventTimeFormat is the RFC2822-style format AWS Health uses for time
// fields. The trailing GMT is a literal rather than Go's MST zone token, so
// time.Parse returns UTC instants and the difference between two parsed values
// is zone-independent.
const healthEventTimeFormat = "Mon, 2 Jan 2006 15:04:05 GMT"

// outageDuration returns endRaw minus startRaw as a signed number of seconds.
//
// Both inputs must be non-empty and parseable with healthEventTimeFormat;
// anything else yields a *processingError carrying causeBadTimestamp. An absent
// JSON field and an explicit "" are indistinguishable after unmarshal and are
// treated identically.
//
// The returned value is used exactly as computed: equal instants give 0, an
// inverted range gives a negative value, and no rounding, clamping, math.Abs,
// unit conversion, or default substitution is applied.
func outageDuration(startRaw, endRaw string) (float64, error) {
	start, err := parseHealthEventTime("startTime", startRaw)
	if err != nil {
		return 0, err
	}

	end, err := parseHealthEventTime("endTime", endRaw)
	if err != nil {
		return 0, err
	}

	return end.Sub(start).Seconds(), nil
}

// parseHealthEventTime parses one AWS Health timestamp with the RFC2822 layout,
// reporting an empty or rejected value as a causeBadTimestamp failure.
func parseHealthEventTime(field, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, &processingError{
			Cause: causeBadTimestamp,
			Err:   fmt.Errorf("%s is absent or empty", field),
		}
	}

	parsed, err := time.Parse(healthEventTimeFormat, raw)
	if err != nil {
		return time.Time{}, &processingError{
			Cause: causeBadTimestamp,
			Err:   fmt.Errorf("%s %q is not in format %q: %w", field, raw, healthEventTimeFormat, err),
		}
	}

	return parsed, nil
}
