# test — run the full Go test suite plus the OpenAI Python SDK compatibility smoke
.PHONY: test test-go test-sdk build vet fmt

test: vet test-go

vet:
	go vet ./...

build:
	go build ./...

test-go:
	go test ./... -count=1

# Requires python + the `openai` package. Skips the SDK test automatically if absent.
test-sdk:
	go test ./internal/app -count=1 -run TestOpenAIPythonSDKCompatibility -v

fmt:
	gofmt -w internal/app
