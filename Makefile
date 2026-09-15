GO ?= go
BIN := bin/opens3

.PHONY: build test vet ci run clean fmt docker release-check release-snapshot

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/opens3

docker:
	docker build --build-arg VERSION=$(VERSION) -t opens3:$(VERSION) .

# Release tooling (GoReleaser v2, run via `go run` so nothing needs
# installing; the first run compiles it). Tagged releases are built by
# .github/workflows/release.yml, see docs/RELEASING.md.
#   make release-check      validate .goreleaser.yaml
#   make release-snapshot   build dist/ (archives, checksums) for this commit
#                           without publishing, signing or SBOMs;
#                           RELEASE_DOCKER=1 also builds the container images
#                           (needs buildx with linux/arm64 support).
GORELEASER_VERSION ?= v2.18.1
GORELEASER := $(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
RELEASE_SKIP := publish,sign,sbom
ifneq ($(RELEASE_DOCKER),1)
RELEASE_SKIP := $(RELEASE_SKIP),docker
endif

release-check:
	$(GORELEASER) check

release-snapshot:
	$(GORELEASER) release --snapshot --clean --skip=$(RELEASE_SKIP)

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

# Master key rotation lifecycle (needs Docker): server and `opens3 master`
# from the repository Dockerfile, data written and verified by
# examples/python/encrypted_data.py in a python container.
.PHONY: rotation-test
rotation-test:
	tests/rotation/run.sh

# MinIO to OpenS3 migration, as the manual describes it, against a real
# MinIO (needs Docker; pulls a pinned MinIO release from quay.io).
.PHONY: migration-test migration-test-silo
migration-test:
	tests/migration/run.sh

# The same procedure from Silo, the maintained MinIO fork (built from
# source at a pinned release, with its client mcli).
migration-test-silo:
	MIGRATION_SOURCE=silo tests/migration/run.sh

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
