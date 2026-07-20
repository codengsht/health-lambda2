package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MetricEmitter matches ddlambda.Metric without coupling the application
// package to Datadog's transport implementation.
type MetricEmitter func(name string, value float64, tags ...string)

// Handler implements lambda.Handler and processes one EventBridge event per
// invocation. It is immutable after construction and safe for concurrent use
// when the supplied writer and metric emitter are safe for concurrent use.
type Handler struct {
	output     io.Writer
	emitMetric MetricEmitter
}

func NewHandler(output io.Writer, emitMetric MetricEmitter) *Handler {
	if output == nil {
		panic("health: output writer is required")
	}
	if emitMetric == nil {
		panic("health: metric emitter is required")
	}
	return &Handler{output: output, emitMetric: emitMetric}
}

// Invoke deliberately receives untouched bytes. Envelope decoding therefore
// happens inside the application and malformed invocations can still produce a
// structured failure log before Lambda retries or routes them to a destination.
func (handler *Handler) Invoke(ctx context.Context, payload []byte) ([]byte, error) {
	if err := handler.Handle(ctx, json.RawMessage(payload)); err != nil {
		return nil, err
	}
	return nil, nil
}

func (handler *Handler) Handle(_ context.Context, raw json.RawMessage) error {
	envelope, err := decodeEnvelope(raw)
	if err != nil {
		return handler.fail(
			failureStageEnvelopeParse,
			"Failed to parse EventBridge envelope",
			nil,
			nil,
			err,
		)
	}

	required, optional, err := decodeDetail(envelope.Detail)
	if err != nil {
		return handler.fail(
			failureStageDetailParse,
			"Failed to parse AWS Health event detail",
			&envelope,
			nil,
			err,
		)
	}

	invalidFields := validateRequired(required)
	if len(invalidFields) > 0 {
		return handler.fail(
			failureStageValidation,
			"AWS Health event failed validation",
			&envelope,
			invalidFields,
			fmt.Errorf("missing or invalid required fields: %s", strings.Join(invalidFields, ", ")),
		)
	}

	if err := writeJSONLine(handler.output, buildSuccessRecord(envelope, required, optional)); err != nil {
		return fmt.Errorf("write AWS Health success log: %w", err)
	}

	handler.emitMetricSafely(
		notificationActivityMetric,
		1,
		"aws_service:"+required.Service,
		"event_type_category:"+required.EventTypeCategory,
		"event_scope_code:"+required.EventScopeCode,
		"health_status:"+required.StatusCode,
		"event_region:"+required.EventRegion,
	)

	return nil
}

func (handler *Handler) fail(
	stage string,
	message string,
	envelope *eventBridgeEnvelope,
	invalidFields []string,
	cause error,
) error {
	record := buildFailureRecord(stage, message, envelope, invalidFields)
	if err := writeJSONLine(handler.output, record); err != nil {
		return errors.Join(cause, fmt.Errorf("write AWS Health failure log: %w", err))
	}
	return cause
}

func (handler *Handler) emitMetricSafely(name string, value float64, tags ...string) {
	defer func() {
		_ = recover()
	}()
	handler.emitMetric(name, value, tags...)
}
