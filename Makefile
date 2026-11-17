CHECK_GO := $(shell command -v go 2> /dev/null)
CHECK_MOCKERY := $(shell command -v mockery 2> /dev/null)
CGO_ENABLED ?= 0

.PHONY: help check-go check-mockery fmt tidy test generate generate-mocks

help:
	@echo "Targets:"
	@echo "  make fmt              Format Go files"
	@echo "  make tidy             Run go mod tidy"
	@echo "  make test             Run all tests"
	@echo "  make generate         Generate all generated code"
	@echo "  make generate-mocks   Generate mocks with mockery"

check-go:
ifndef CHECK_GO
	$(error "Go is not installed. Please install Go and retry.")
endif

check-mockery:
ifndef CHECK_MOCKERY
	$(error "Mockery is not installed. Please install Mockery and retry.")
endif

fmt: check-go
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

tidy: check-go
	go mod tidy

test: check-go
	CGO_ENABLED=$(CGO_ENABLED) go test ./...

generate: generate-mocks

generate-mocks: check-mockery
	mockery
