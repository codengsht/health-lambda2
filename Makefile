LAMBDA_ARCH ?= arm64
BUILD_DIR ?= build
COMMAND := ./cmd/aws-health-event-lambda

.PHONY: test vet verify build

test:
	go test ./...

vet:
	go vet ./...

verify:
	go test -race ./...
	go vet ./...

build:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=$(LAMBDA_ARCH) CGO_ENABLED=0 go build \
		-trimpath -ldflags="-s -w" \
		-o $(BUILD_DIR)/bootstrap $(COMMAND)
