GO ?= go
BIN := bin/opens3

.PHONY: build test vet ci run clean fmt docker

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/opens3

docker:
	docker build --build-arg VERSION=$(VERSION) -t opens3:$(VERSION) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

ci: vet
	$(GO) test -race ./...

run: build
	$(BIN) server --root ./data

clean:
	rm -rf bin dist

# Ceph s3-tests conformance suite (needs Docker). Extra pytest args via
# S3TESTS_ARGS, e.g. make conformance S3TESTS_ARGS='-k test_bucket_list'.
.PHONY: conformance
conformance:
	tests/s3tests/run.sh $(S3TESTS_ARGS)
