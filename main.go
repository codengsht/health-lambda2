// Package main is the entry point for the aws-health-event-forwarder AWS Lambda
// function. It wires the Datadog Lambda wrapper around the handler in
// internal/forwarder and starts the custom runtime; all behaviour lives in that
// package.
package main

import (
	ddlambda "github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2"
	"github.com/aws/aws-lambda-go/lambda"

	"prodgitlab.usaa.com/grp-cloud-aws-pce-appps/aws-health-event-forwarder/internal/forwarder"
)

// main starts the Lambda runtime. The nil ddlambda config keeps every Datadog
// setting — API key, site, endpoint — in the Datadog Lambda layer's hands, so
// no credential or endpoint value appears in this repository.
func main() {
	lambda.Start(ddlambda.WrapFunction(forwarder.Handle, nil))
}
