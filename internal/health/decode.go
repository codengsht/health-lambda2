package health

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func decodeEnvelope(raw json.RawMessage) (eventBridgeEnvelope, error) {
	if !isJSONObject(raw) {
		return eventBridgeEnvelope{}, fmt.Errorf("EventBridge envelope must be a JSON object")
	}

	var envelope eventBridgeEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return eventBridgeEnvelope{}, fmt.Errorf("decode EventBridge envelope: %w", err)
	}

	return envelope, nil
}

func decodeDetail(raw json.RawMessage) (requiredDetail, optionalDetail, error) {
	if !isJSONObject(raw) {
		return requiredDetail{}, optionalDetail{}, fmt.Errorf("AWS Health detail must be a JSON object")
	}

	var required requiredDetail
	if err := json.Unmarshal(raw, &required); err != nil {
		return requiredDetail{}, optionalDetail{}, fmt.Errorf("decode required AWS Health fields: %w", err)
	}

	var rawOptional optionalDetailRaw
	if err := json.Unmarshal(raw, &rawOptional); err != nil {
		// The detail was already decoded as a valid object above. Keep this guard
		// explicit in case optionalDetailRaw gains a non-RawMessage field later.
		return requiredDetail{}, optionalDetail{}, fmt.Errorf("decode optional AWS Health fields: %w", err)
	}

	return required, decodeOptional(rawOptional), nil
}

func decodeOptional(raw optionalDetailRaw) optionalDetail {
	var optional optionalDetail

	optional.EndTime = decodeOptionalString(raw.EndTime)
	optional.LastUpdatedTime = decodeOptionalString(raw.LastUpdatedTime)
	optional.BackupEvent = decodeOptionalString(raw.BackupEvent)
	optional.Page = decodeOptionalString(raw.Page)
	optional.TotalPages = decodeOptionalString(raw.TotalPages)
	optional.Actionability = decodeOptionalString(raw.Actionability)

	if account := decodeOptionalString(raw.AffectedAccount); account != nil && validAWSAccountID(*account) {
		optional.AffectedAccount = account
	}

	if description, ok := selectLatestDescription(raw.EventDescription); ok {
		optional.LatestDescription = &description
	}

	if isJSONArray(raw.AffectedEntities) {
		optional.AffectedEntities = bytes.Clone(raw.AffectedEntities)
	}

	if personas, ok := decodeStringArray(raw.Personas); ok {
		optional.Personas = personas
	}

	return optional
}

func decodeOptionalString(raw json.RawMessage) *string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return &value
}

func decodeStringArray(raw json.RawMessage) ([]string, bool) {
	if !isJSONArray(raw) {
		return nil, false
	}

	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

func selectLatestDescription(raw json.RawMessage) (string, bool) {
	if !isJSONArray(raw) {
		return "", false
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", false
	}

	var fallback string
	for _, entry := range entries {
		if !isJSONObject(entry) {
			continue
		}

		var fields struct {
			Language          json.RawMessage `json:"language"`
			LatestDescription json.RawMessage `json:"latestDescription"`
		}
		if err := json.Unmarshal(entry, &fields); err != nil {
			continue
		}

		description := decodeOptionalString(fields.LatestDescription)
		if description == nil || strings.TrimSpace(*description) == "" {
			continue
		}

		language := decodeOptionalString(fields.Language)
		if language != nil && *language == "en_US" {
			return *description, true
		}
		if fallback == "" {
			fallback = *description
		}
	}

	return fallback, fallback != ""
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '[' && json.Valid(trimmed)
}

func validAWSAccountID(value string) bool {
	if len(value) != 12 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
