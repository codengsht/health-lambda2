package main

import (
	"os"

	ddlambda "github.com/DataDog/dd-trace-go/contrib/aws/datadog-lambda-go/v2"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/frknio/health-lambda2/internal/health"
)

func main() {
	handler := health.NewHandler(os.Stdout, ddlambda.Metric)
	lambda.StartHandler(ddlambda.WrapLambdaHandlerInterface(handler, nil))
}
