package health

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDecodeEnvelopeRequiresJSONObject(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `"event"`, "42", "true", "{"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := decodeEnvelope(json.RawMessage(raw)); err == nil {
				t.Fatalf("decodeEnvelope(%q) error = nil", raw)
			}
		})
	}
}

func TestSelectLatestDescriptionSkipsMalformedEntries(t *testing.T) {
	raw := json.RawMessage(`[
		null,
		42,
		{"language":"fr_FR","latestDescription":"fallback"},
		{"language":false,"latestDescription":[]},
		{"language":"en_US","latestDescription":"preferred"}
	]`)

	got, ok := selectLatestDescription(raw)
	if !ok || got != "preferred" {
		t.Fatalf("selectLatestDescription() = %q, %v; want preferred, true", got, ok)
	}
}

func TestDecodeOptionalPreservesOnlyCompatibleValues(t *testing.T) {
	raw := optionalDetailRaw{
		EndTime:          json.RawMessage(`"Fri, 27 Jan 2023 09:01:22 GMT"`),
		LastUpdatedTime:  json.RawMessage(`12`),
		AffectedEntities: json.RawMessage(`[]`),
		AffectedAccount:  json.RawMessage(`"123456789012"`),
		Personas:         json.RawMessage(`["OPERATIONAL","BILLING"]`),
	}

	got := decodeOptional(raw)
	if got.EndTime == nil || *got.EndTime != "Fri, 27 Jan 2023 09:01:22 GMT" {
		t.Errorf("end time = %v", got.EndTime)
	}
	if got.LastUpdatedTime != nil {
		t.Errorf("last updated time = %v, want nil", got.LastUpdatedTime)
	}
	if string(got.AffectedEntities) != "[]" {
		t.Errorf("affected entities = %q", got.AffectedEntities)
	}
	if got.AffectedAccount == nil || *got.AffectedAccount != "123456789012" {
		t.Errorf("affected account = %v", got.AffectedAccount)
	}
	if !reflect.DeepEqual(got.Personas, []string{"OPERATIONAL", "BILLING"}) {
		t.Errorf("personas = %#v", got.Personas)
	}
}

func TestValidAWSAccountID(t *testing.T) {
	tests := map[string]bool{
		"123456789012": true,
		"":             false,
		"123":          false,
		"12345678901x": false,
		" 23456789012": false,
	}
	for value, want := range tests {
		if got := validAWSAccountID(value); got != want {
			t.Errorf("validAWSAccountID(%q) = %v, want %v", value, got, want)
		}
	}
}
