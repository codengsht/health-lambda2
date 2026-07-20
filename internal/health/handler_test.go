package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambda"
)

var _ lambda.Handler = (*Handler)(nil)

type capturedMetric struct {
	name  string
	value float64
	tags  []string
}

func TestHandlerProcessesValidEvent(t *testing.T) {
	payload := readValidEvent(t)
	var output bytes.Buffer
	var metrics []capturedMetric
	handler := NewHandler(&output, func(name string, value float64, tags ...string) {
		metrics = append(metrics, capturedMetric{name: name, value: value, tags: append([]string(nil), tags...)})
	})

	response, err := handler.Invoke(context.Background(), payload)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if response != nil {
		t.Fatalf("Invoke() response = %q, want nil", response)
	}
	if got := strings.Count(output.String(), "\n"); got != 1 {
		t.Fatalf("physical log lines = %d, want 1; output = %q", got, output.String())
	}

	var record successRecord
	decodeLogLine(t, output.Bytes(), &record)
	if record.RecordType != successRecordType {
		t.Errorf("record_type = %q, want %q", record.RecordType, successRecordType)
	}
	if record.EventBridge.ReceivingAccount != "123456789012" {
		t.Errorf("receiving account = %q", record.EventBridge.ReceivingAccount)
	}
	if record.EventBridge.DeliveryRegion != "us-east-1" {
		t.Errorf("delivery region = %q", record.EventBridge.DeliveryRegion)
	}
	if record.AWSHealth.EventRegion != "af-south-1" {
		t.Errorf("impacted region = %q", record.AWSHealth.EventRegion)
	}
	if record.AWSHealth.AffectedAccount == nil || *record.AWSHealth.AffectedAccount != "210987654321" {
		t.Errorf("affected account = %v", record.AWSHealth.AffectedAccount)
	}
	if record.AWSHealth.LatestDescription == nil || *record.AWSHealth.LatestDescription != "[RESOLVED]\nThe service has recovered." {
		t.Errorf("latest description = %v", record.AWSHealth.LatestDescription)
	}
	if record.AWSHealth.DurationSeconds == nil || *record.AWSHealth.DurationSeconds != 10_711 {
		t.Errorf("duration seconds = %v, want 10711", record.AWSHealth.DurationSeconds)
	}
	if record.AWSHealth.DurationCalculationStatus != "calculated" {
		t.Errorf("duration status = %q", record.AWSHealth.DurationCalculationStatus)
	}
	if record.AWSHealth.Actionability == nil || *record.AWSHealth.Actionability != "INFORMATIONAL" {
		t.Errorf("actionability = %v", record.AWSHealth.Actionability)
	}
	if !reflect.DeepEqual(record.AWSHealth.Personas, []string{"OPERATIONS"}) {
		t.Errorf("personas = %#v", record.AWSHealth.Personas)
	}
	if string(record.AWSHealth.AffectedEntities) == "" {
		t.Error("affected_entities was omitted")
	}

	wantMetric := capturedMetric{
		name:  notificationActivityMetric,
		value: 1,
		tags: []string{
			"aws_service:EC2",
			"event_type_category:issue",
			"event_scope_code:PUBLIC",
			"health_status:closed",
			"event_region:af-south-1",
		},
	}
	if !reflect.DeepEqual(metrics, []capturedMetric{wantMetric}) {
		t.Fatalf("metrics = %#v, want %#v", metrics, []capturedMetric{wantMetric})
	}
}

func TestHandlerRejectsMalformedEnvelopeWithoutLoggingPayload(t *testing.T) {
	payload := []byte(`{"secret":"do-not-log","detail":`)
	var output bytes.Buffer
	metricCalls := 0
	handler := NewHandler(&output, func(string, float64, ...string) { metricCalls++ })

	if _, err := handler.Invoke(context.Background(), payload); err == nil {
		t.Fatal("Invoke() error = nil, want envelope parse error")
	}
	if metricCalls != 0 {
		t.Fatalf("metric calls = %d, want 0", metricCalls)
	}
	if strings.Contains(output.String(), "do-not-log") {
		t.Fatalf("failure log contains raw payload: %s", output.String())
	}

	var record failureRecord
	decodeLogLine(t, output.Bytes(), &record)
	if record.FailureStage != "envelope_parse" {
		t.Errorf("failure_stage = %q", record.FailureStage)
	}
	if record.EventBridge != nil {
		t.Errorf("eventbridge = %#v, want nil", record.EventBridge)
	}
}

func TestHandlerClassifiesDetailFailuresAndIncludesEnvelopeDiagnostics(t *testing.T) {
	tests := map[string]func(map[string]any){
		"missing detail": func(envelope map[string]any) {
			delete(envelope, "detail")
		},
		"null detail": func(envelope map[string]any) {
			envelope["detail"] = nil
		},
		"non-object detail": func(envelope map[string]any) {
			envelope["detail"] = []any{}
		},
		"wrong required type": func(envelope map[string]any) {
			detail := envelope["detail"].(map[string]any)
			detail["service"] = 42
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			payload := mutateValidEvent(t, mutate)
			var output bytes.Buffer
			metricCalls := 0
			handler := NewHandler(&output, func(string, float64, ...string) { metricCalls++ })

			if err := handler.Handle(context.Background(), payload); err == nil {
				t.Fatal("Handle() error = nil, want detail parse error")
			}
			if metricCalls != 0 {
				t.Fatalf("metric calls = %d, want 0", metricCalls)
			}

			var record failureRecord
			decodeLogLine(t, output.Bytes(), &record)
			if record.FailureStage != "detail_parse" {
				t.Errorf("failure_stage = %q", record.FailureStage)
			}
			if record.EventBridge == nil || record.EventBridge.ID != "7bf73129-1428-4cd3-a780-95db273d1602" {
				t.Errorf("eventbridge diagnostics = %#v", record.EventBridge)
			}
		})
	}
}

func TestHandlerReportsEveryInvalidRequiredField(t *testing.T) {
	payload := mutateValidEvent(t, func(envelope map[string]any) {
		detail := envelope["detail"].(map[string]any)
		detail["eventArn"] = "  "
		detail["service"] = ""
		detail["startTime"] = "not a timestamp"
		delete(detail, "eventRegion")
	})
	var output bytes.Buffer
	metricCalls := 0
	handler := NewHandler(&output, func(string, float64, ...string) { metricCalls++ })

	if err := handler.Handle(context.Background(), payload); err == nil {
		t.Fatal("Handle() error = nil, want validation error")
	}
	if metricCalls != 0 {
		t.Fatalf("metric calls = %d, want 0", metricCalls)
	}

	var record failureRecord
	decodeLogLine(t, output.Bytes(), &record)
	want := []string{"eventArn", "service", "startTime", "eventRegion"}
	if record.FailureStage != "validation" {
		t.Errorf("failure_stage = %q", record.FailureStage)
	}
	if !reflect.DeepEqual(record.MissingOrInvalidFields, want) {
		t.Errorf("invalid fields = %#v, want %#v", record.MissingOrInvalidFields, want)
	}
}

func TestHandlerDropsMalformedOptionalFields(t *testing.T) {
	payload := mutateValidEvent(t, func(envelope map[string]any) {
		detail := envelope["detail"].(map[string]any)
		detail["endTime"] = 123
		detail["lastUpdatedTime"] = map[string]any{}
		detail["affectedAccount"] = true
		detail["eventDescription"] = "wrong"
		detail["affectedEntities"] = map[string]any{}
		detail["backupEvent"] = false
		detail["page"] = 1
		detail["totalPages"] = 2
		detail["actionability"] = []any{}
		detail["personas"] = "OPERATIONS"
	})
	var output bytes.Buffer
	metricCalls := 0
	handler := NewHandler(&output, func(string, float64, ...string) { metricCalls++ })

	if err := handler.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if metricCalls != 1 {
		t.Fatalf("metric calls = %d, want 1", metricCalls)
	}

	var raw struct {
		AWSHealth map[string]json.RawMessage `json:"aws_health"`
	}
	decodeLogLine(t, output.Bytes(), &raw)
	for _, field := range []string{
		"end_time", "last_updated_time", "affected_account", "latest_description",
		"affected_entities", "backup_event", "page", "total_pages", "actionability", "personas",
	} {
		if _, exists := raw.AWSHealth[field]; exists {
			t.Errorf("schema-incompatible optional field %q was not omitted", field)
		}
	}
	if got := string(raw.AWSHealth["duration_calculation_status"]); got != `"missing_end_time"` {
		t.Errorf("duration status = %s", got)
	}
}

func TestHandlerSuppressesMetricWhenSuccessLogWriteFails(t *testing.T) {
	metricCalls := 0
	handler := NewHandler(errorWriter{err: errors.New("disk full")}, func(string, float64, ...string) {
		metricCalls++
	})

	err := handler.Handle(context.Background(), readValidEvent(t))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Handle() error = %v, want write failure", err)
	}
	if metricCalls != 0 {
		t.Fatalf("metric calls = %d, want 0", metricCalls)
	}
}

func TestHandlerJoinsFailureLogWriteErrorWithProcessingError(t *testing.T) {
	handler := NewHandler(errorWriter{err: errors.New("stdout unavailable")}, func(string, float64, ...string) {})

	err := handler.Handle(context.Background(), json.RawMessage(`null`))
	if err == nil {
		t.Fatal("Handle() error = nil")
	}
	for _, fragment := range []string{"JSON object", "stdout unavailable"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Handle() error %q does not contain %q", err, fragment)
		}
	}
}

func TestHandlerRecoversMetricPanic(t *testing.T) {
	var output bytes.Buffer
	handler := NewHandler(&output, func(string, float64, ...string) {
		panic("metric transport panic")
	})

	if err := handler.Handle(context.Background(), readValidEvent(t)); err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("output = %q", output.String())
	}
}

func TestHandlerDoesNotDeduplicateInvocations(t *testing.T) {
	var output bytes.Buffer
	metricCalls := 0
	handler := NewHandler(&output, func(string, float64, ...string) { metricCalls++ })
	payload := readValidEvent(t)

	for invocation := 0; invocation < 2; invocation++ {
		if err := handler.Handle(context.Background(), payload); err != nil {
			t.Fatalf("invocation %d: Handle() error = %v", invocation, err)
		}
	}
	if got := strings.Count(output.String(), "\n"); got != 2 {
		t.Errorf("log lines = %d, want 2", got)
	}
	if metricCalls != 2 {
		t.Errorf("metric calls = %d, want 2", metricCalls)
	}
}

func TestNewHandlerRequiresDependencies(t *testing.T) {
	tests := map[string]func(){
		"writer": func() { NewHandler(nil, func(string, float64, ...string) {}) },
		"metric": func() { NewHandler(io.Discard, nil) },
	}
	for name, construct := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("constructor did not panic")
				}
			}()
			construct()
		})
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

func readValidEvent(t *testing.T) json.RawMessage {
	t.Helper()
	payload, err := os.ReadFile("testdata/public-health-event.json")
	if err != nil {
		t.Fatalf("read test payload: %v", err)
	}
	return payload
}

func mutateValidEvent(t *testing.T, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(readValidEvent(t), &event); err != nil {
		t.Fatalf("decode test payload: %v", err)
	}
	mutate(event)
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode mutated payload: %v", err)
	}
	return payload
}

func decodeLogLine(t *testing.T, line []byte, destination any) {
	t.Helper()
	if !bytes.HasSuffix(line, []byte{'\n'}) {
		t.Fatalf("structured log has no trailing newline: %q", line)
	}
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), destination); err != nil {
		t.Fatalf("decode structured log: %v; log = %q", err, line)
	}
}
