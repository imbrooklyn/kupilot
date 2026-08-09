GO_CMD ?= go
GOFMT_CMD ?= gofmt
PACKAGES ?= ./...
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/kupilot
CROSS_BIN_DIR ?= $(BIN_DIR)/cross
COMMAND_PACKAGE ?= ./cmd/kupilot

.PHONY: fmt fmt-check test test-race vet build cross-build check check-all

fmt:
	$(GO_CMD) fmt $(PACKAGES)

fmt-check:
	@unformatted="$$(find . -type f -name '*.go' \
		-not -path './vendor/*' \
		-not -path './.git/*' \
		-exec $(GOFMT_CMD) -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
		printf '%s\n' 'The following Go files need formatting:' "$$unformatted"; \
		exit 1; \
	fi

test:
	$(GO_CMD) test -count=1 $(PACKAGES)

test-race:
	$(GO_CMD) test -race -count=1 $(PACKAGES)

vet:
	$(GO_CMD) vet $(PACKAGES)

build:
	mkdir -p "$(dir $(BINARY))"
	$(GO_CMD) build -o "$(BINARY)" $(COMMAND_PACKAGE)

cross-build:
	mkdir -p "$(CROSS_BIN_DIR)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-darwin-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-darwin-arm64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-linux-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-linux-arm64" $(COMMAND_PACKAGE)

check: fmt-check test vet build

check-all: check test-race cross-build
