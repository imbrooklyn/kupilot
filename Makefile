GO_CMD ?= go
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/kupilot

.PHONY: fmt test vet build

fmt:
	$(GO_CMD) fmt ./...

test:
	$(GO_CMD) test ./...

vet:
	$(GO_CMD) vet ./...

build:
	mkdir -p $(dir $(BINARY))
	$(GO_CMD) build -o $(BINARY) ./cmd/kupilot
