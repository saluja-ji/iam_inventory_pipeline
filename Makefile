.PHONY: help build package test lint fmt schema tf-validate local-demo clean

BUILD_DIR := build/lambda
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

help:
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "%-14s %s\n", $$1, $$2}'

test: ## Unit tests with the race detector
	go vet ./...
	go test -race -cover ./...

lint: ## golangci-lint + terraform fmt/validate
	golangci-lint run ./...
	terraform fmt -check -recursive terraform

fmt: ## Format Go and Terraform
	gofmt -w cmd internal
	terraform fmt -recursive terraform

schema: ## Regenerate Glue column schemas from the Go row structs
	go run ./cmd/schemagen

build: ## Cross-compile the Lambda for linux/arm64 (provided.al2023)
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/bootstrap ./cmd/lambda

package: build ## Binary + collector config, ready for Terraform's archive_file
	cp otel/collector.yaml $(BUILD_DIR)/collector.yaml
	chmod 755 $(BUILD_DIR)/bootstrap

tf-validate: ## Validate every Terraform root without a backend
	for d in terraform/bootstrap terraform/envs/dev; do \
		(cd $$d && terraform init -backend=false -input=false >/dev/null && terraform validate) || exit 1; \
	done

local-demo: ## Run the whole pipeline offline on sample data, with console tracing
	OTEL_TRACES_EXPORTER=console go run ./cmd/local -fixture testdata/sample_iam.json -out out

clean:
	rm -rf build out terraform/envs/dev/.build
