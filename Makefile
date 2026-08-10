GO_CMD ?= go
GOFMT_CMD ?= gofmt
PACKAGES ?= ./...
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/kupilot
CROSS_BIN_DIR ?= $(BIN_DIR)/cross
COMMAND_PACKAGE ?= ./cmd/kupilot
TOOLS_BIN_DIR ?= $(BIN_DIR)/tools
GATE_GO_VERSION ?= go1.25.12

GOIMPORTS_VERSION ?= v0.48.0
GOLANGCI_LINT_VERSION ?= v2.11.4
GOVULNCHECK_VERSION ?= v1.6.0
ACTIONLINT_VERSION ?= v1.7.12

GOIMPORTS_BIN := $(TOOLS_BIN_DIR)/goimports/$(GOIMPORTS_VERSION)/goimports
GOLANGCI_LINT_BIN := $(TOOLS_BIN_DIR)/golangci-lint/$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK_BIN := $(TOOLS_BIN_DIR)/govulncheck/$(GOVULNCHECK_VERSION)/govulncheck
ACTIONLINT_BIN := $(TOOLS_BIN_DIR)/actionlint/$(ACTIONLINT_VERSION)/actionlint

SECURITY_TEST_PATTERN := ^(TestRedactor.*|TestOutputGuard.*|TestScopeManager.*|TestSlashRegistryIsFixedAndReadOnly|TestParseSlashDraftRules|TestValidateModelEndpointPolicy|TestRedirectPolicyAllowsOnlyCanonicalOrigin|TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove|TestDatabaseAndWALExcludeProhibitedContentCanaries|TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow|TestSanitizeExternalText.*|TestApplicationTextAndScopeAreSanitizedBeforeRenderState|TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler|TestDiagnosisValidatorSanitizesEveryModelFreeTextField|TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence|TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction|TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis|TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink)$$
MIGRATION_TEST_PATTERN := ^(TestMigrate.*|TestInitialSchemaContainsOnlyAllowlistedStorageColumns)$$
E2E_TEST_PATTERN := ^(TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis|TestSessionApplicationAdapterUsesRealSQLiteResumeEligibility|TestResumeIntegrationUsesTemporaryDatabaseAndRevalidatesOnlyAcceptedScope|TestDiagnosisScenarioFixtures)$$
FUZZ_SEED_PATTERN := ^(FuzzRedactorSafety|FuzzBindToolCallStrictSchema|FuzzBoundedSSEBodyIsChunkIndependent|FuzzParseSlashDraftHasNoDynamicAuthority)$$
IMPORT_GUARD_PATTERN := ^(TestApplicationImportBoundaryIsStatic|TestEinoImportsRemainInTheirSoleTranslationBoundaries|TestExportedKubeBoundaryContainsNoClientGoTypes|TestReadOnlyToolPathContainsNoWriteShellOrGenericKubernetesEscape|TestTUIImportBoundaryAndSingleTextareaAreStatic|TestRepositorySourcesKeepExplicitSQLBoundary|TestSQLXImportRemainsInsideSQLiteAdapter|TestCompositionConstructsOneModelLifecycleAndNoWritePath|TestSelectedDriverAndSQLXContract|TestV01ProductionHasNoApprovalServiceOrWriteExecutor)$$

.PHONY: bootstrap-tools go-version-check fmt imports fmt-check imports-check lint workflow-lint test test-race test-security security test-migration test-e2e test-fuzz-seeds import-guard dependency-guard vuln vet build cross-build platform-smoke check check-slow check-all

$(GOIMPORTS_BIN):
	mkdir -p "$(dir $@)"
	GOBIN="$(abspath $(dir $@))" $(GO_CMD) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

$(GOLANGCI_LINT_BIN):
	mkdir -p "$(dir $@)"
	GOBIN="$(abspath $(dir $@))" $(GO_CMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK_BIN):
	mkdir -p "$(dir $@)"
	GOBIN="$(abspath $(dir $@))" $(GO_CMD) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(ACTIONLINT_BIN):
	mkdir -p "$(dir $@)"
	GOBIN="$(abspath $(dir $@))" $(GO_CMD) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

bootstrap-tools: $(GOIMPORTS_BIN) $(GOLANGCI_LINT_BIN) $(GOVULNCHECK_BIN) $(ACTIONLINT_BIN)

go-version-check:
	@actual="$$( $(GO_CMD) env GOVERSION )"; \
	if [ "$$actual" != "$(GATE_GO_VERSION)" ]; then \
		printf '%s\n' "CI gates require $(GATE_GO_VERSION); found $$actual."; \
		exit 1; \
	fi

fmt: imports

imports: $(GOIMPORTS_BIN)
	find . -type f -name '*.go' \
		-not -path './vendor/*' \
		-not -path './.git/*' \
		-exec "$(GOIMPORTS_BIN)" -w {} +

fmt-check:
	@unformatted="$$(find . -type f -name '*.go' \
		-not -path './vendor/*' \
		-not -path './.git/*' \
		-exec $(GOFMT_CMD) -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
		printf '%s\n' 'The following Go files need formatting:' "$$unformatted"; \
		exit 1; \
	fi

imports-check: $(GOIMPORTS_BIN)
	@unformatted="$$(find . -type f -name '*.go' \
		-not -path './vendor/*' \
		-not -path './.git/*' \
		-exec "$(GOIMPORTS_BIN)" -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
		printf '%s\n' 'The following Go files need goimports:' "$$unformatted"; \
		exit 1; \
	fi

lint: $(GOLANGCI_LINT_BIN)
	"$(GOLANGCI_LINT_BIN)" run --config .golangci.yml $(PACKAGES)

workflow-lint: $(ACTIONLINT_BIN)
	"$(ACTIONLINT_BIN)" .github/workflows/ci.yml

test:
	$(GO_CMD) test -count=1 $(PACKAGES)

test-race:
	$(GO_CMD) test -race -count=1 $(PACKAGES)

test-security:
	$(GO_CMD) test -count=1 $(PACKAGES) -run '$(SECURITY_TEST_PATTERN)'

security: test-security dependency-guard

test-migration:
	$(GO_CMD) test -count=1 ./internal/persistence/sqlite -run '$(MIGRATION_TEST_PATTERN)'

test-e2e:
	$(GO_CMD) test -count=1 $(PACKAGES) -run '$(E2E_TEST_PATTERN)'

test-fuzz-seeds:
	$(GO_CMD) test -count=1 $(PACKAGES) -run '$(FUZZ_SEED_PATTERN)'

import-guard:
	$(GO_CMD) test -count=1 $(PACKAGES) -run '$(IMPORT_GUARD_PATTERN)'

dependency-guard: import-guard
	$(GO_CMD) mod verify

vuln: $(GOVULNCHECK_BIN)
	"$(GOVULNCHECK_BIN)" $(PACKAGES)

vet:
	$(GO_CMD) vet $(PACKAGES)

build:
	mkdir -p "$(dir $(BINARY))"
	CGO_ENABLED=0 $(GO_CMD) build -o "$(BINARY)" $(COMMAND_PACKAGE)

cross-build:
	mkdir -p "$(CROSS_BIN_DIR)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-darwin-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-darwin-arm64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-linux-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO_CMD) build -o "$(CROSS_BIN_DIR)/kupilot-linux-arm64" $(COMMAND_PACKAGE)

platform-smoke: go-version-check test build

check: go-version-check fmt-check imports-check vet lint workflow-lint test build

check-slow: go-version-check test-race security test-migration test-e2e test-fuzz-seeds vuln cross-build

check-all: check check-slow
