GO ?= go
BIN := bin/opens3

.PHONY: build test vet ci run clean fmt

build:
	$(GO) build -o $(BIN) ./cmd/opens3

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
