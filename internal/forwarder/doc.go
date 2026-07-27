// Package forwarder holds the AWS Health Event Forwarder: the Lambda handler
// that receives AWS Health events delivered by EventBridge and submits the
// Datadog distribution metric aws.health.events.duration.
//
// The package is layered so the decision logic can be tested without a Lambda
// runtime or a Datadog endpoint:
//
//	types.go     value types: HealthEventDetail, metricSample, notice, evaluation
//	errors.go    the two named failure causes and processingError
//	duration.go  outageDuration: the AWS Health RFC2822 layout and the second delta
//	tags.go      metricTags and oversizedTags: the five verbatim tag strings
//	evaluate.go  evaluate: the single pure decision function
//	handler.go   Handle: the impure edge (log, submit, return error)
//
// Everything in duration.go, tags.go, and evaluate.go is pure: arguments in,
// values out, no I/O and no package state. Handle is the only function that
// performs side effects, and submitMetric is the only submission point.
package forwarder
