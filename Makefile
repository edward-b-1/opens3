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

# AWS CLI end-to-end suite (needs Docker: runs the pinned amazon/aws-cli
# image against a local server) and regenerates docs/AWSCLI.md.
# OPENS3_AWSCLI=native uses an `aws` on PATH / ~/.local/bin instead.
.PHONY: awscli
awscli:
	tests/awscli/run.sh

# Compile the user manual (docs/manual/src/*.md) into docs/MANUAL.md.
.PHONY: manual
manual:
	docs/manual/build.sh

# Console JavaScript lint: ESLint in the pinned node image (needs Docker).
.PHONY: lint-js
lint-js:
	tests/console/lint.sh

# Console checks: lint-js, then the Playwright browser smoke test against a
# local server (needs Docker). Extra Playwright args via CONSOLE_ARGS,
# e.g. make console-test CONSOLE_ARGS='-g identity'; CONSOLE_BROWSERS=chromium
# skips Firefox and WebKit.
.PHONY: console-test
console-test: lint-js
	tests/console/run.sh $(CONSOLE_ARGS)
