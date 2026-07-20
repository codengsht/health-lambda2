package health

import "testing"

func TestCalculateDuration(t *testing.T) {
	start := "Fri, 27 Jan 2023 06:02:51 GMT"
	validEnd := "Fri, 27 Jan 2023 09:01:22 GMT"
	emptyEnd := ""
	invalidEnd := "later"
	negativeEnd := "Fri, 27 Jan 2023 05:02:51 GMT"

	tests := []struct {
		name        string
		status      string
		start       string
		end         *string
		wantStatus  string
		wantSeconds *int64
	}{
		{name: "closed", status: "closed", start: start, end: &validEnd, wantStatus: "calculated", wantSeconds: int64Pointer(10_711)},
		{name: "closed without end", status: "closed", start: start, wantStatus: "missing_end_time"},
		{name: "closed empty end", status: "closed", start: start, end: &emptyEnd, wantStatus: "invalid_end_time"},
		{name: "closed invalid end", status: "closed", start: start, end: &invalidEnd, wantStatus: "invalid_end_time"},
		{name: "closed negative", status: "closed", start: start, end: &negativeEnd, wantStatus: "negative_duration"},
		{name: "open", status: "open", start: start, end: &validEnd, wantStatus: "not_final"},
		{name: "upcoming", status: "upcoming", start: start, wantStatus: "not_final"},
		{name: "future status", status: "paused", start: start, end: &validEnd, wantStatus: "unsupported_status"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := calculateDuration(requiredDetail{StatusCode: test.status, StartTime: test.start}, test.end)
			if got.Status != test.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, test.wantStatus)
			}
			switch {
			case got.Seconds == nil && test.wantSeconds == nil:
			case got.Seconds == nil || test.wantSeconds == nil:
				t.Errorf("seconds = %v, want %v", got.Seconds, test.wantSeconds)
			case *got.Seconds != *test.wantSeconds:
				t.Errorf("seconds = %d, want %d", *got.Seconds, *test.wantSeconds)
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }
