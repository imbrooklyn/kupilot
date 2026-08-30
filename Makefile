GO_CMD ?= go
GOFMT_CMD ?= gofmt
PACKAGES ?= ./...
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/kupilot
CROSS_BIN_DIR ?= $(BIN_DIR)/cross
COMMAND_PACKAGE ?= ./cmd/kupilot
TOOLS_BIN_DIR ?= $(BIN_DIR)/tools
GATE_GO_VERSION ?= go1.25.13
BINARY_SIZE_BASELINE_DIR ?=

GOIMPORTS_VERSION ?= v0.48.0
GOLANGCI_LINT_VERSION ?= v2.11.4
GOVULNCHECK_VERSION ?= v1.6.0
ACTIONLINT_VERSION ?= v1.7.12
GORELEASER_VERSION ?= v2.13.3
SYFT_VERSION ?= v1.44.0
RELEASE_VERSION ?= 0.3.0

GOIMPORTS_BIN := $(TOOLS_BIN_DIR)/goimports/$(GOIMPORTS_VERSION)/goimports
GOLANGCI_LINT_BIN := $(TOOLS_BIN_DIR)/golangci-lint/$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK_BIN := $(TOOLS_BIN_DIR)/govulncheck/$(GOVULNCHECK_VERSION)/govulncheck
ACTIONLINT_BIN := $(TOOLS_BIN_DIR)/actionlint/$(ACTIONLINT_VERSION)/actionlint
GORELEASER_BIN := $(TOOLS_BIN_DIR)/goreleaser/$(GORELEASER_VERSION)/goreleaser
SYFT_BIN := $(TOOLS_BIN_DIR)/syft/$(SYFT_VERSION)/syft

SECURITY_TEST_PATTERN := ^(TestSecurityAssurance.*|TestRedactor.*|TestOutputGuard.*|TestScopeManager.*|TestSlashRegistryIsFixedAndReadOnly|TestParseSlashDraftRules|TestValidateModelEndpointPolicy|TestRedirectPolicyAllowsOnlyCanonicalOrigin|TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove|TestGPT5CompatibleIdentifierReachesHTTPWithFixedPayload|TestHTTPAndTransportErrorCanariesAreBoundedAndDiscarded|TestHTTPErrorFixturesMapToSafeClassesWithoutBodyLeakage|TestModelRequestErrorMappingUsesObservedHTTPStatusWithoutRetainingRawCause|TestFileLoggerWritesBoundedSafeModelFailureDiagnostics|TestFileLoggerMarksSafeModelCallStackTruncation|TestFileLoggerWritesOnlyExplicitOptInSensitiveModelDiagnostics|TestFileLoggerBoundsOptInSensitiveFields|TestDatabaseAndWALExcludeProhibitedContentCanaries|TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow|TestSanitizeExternalText.*|TestApplicationTextAndScopeAreSanitizedBeforeRenderState|TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler|TestDiagnosisValidatorSanitizesEveryModelFreeTextField|TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence|TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction|TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis|TestPrivacy(ConsentLifecycleBindsOriginCategoriesAndPolicy|ManagerDefersStoreIOUntilModelOriginIsConfigured)|TestModelSetupSecret.*|TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink|TestFileModelAPIKeyCanaryIsExtractedFromOrdinaryConfiguration|TestLoadEnvironmentCredentialOverridesFileAndIsUnsetOnce|TestSaveModelProfile.*|TestEnsureHome.*|TestPublishConfiguration.*|TestClearCache.*|TestCoordinator(UnconfiguredModel|ModelSetup).*|TestUnconfiguredModelSetup.*|TestUnconfiguredModelDoesNotPreemptExplicitResumeStartup|TestModelSlashReconfigures.*|TestInvalidModelCredentialKeepsComposerMaskedAndOutOfHistory|TestOptionalModelSetupCanBeCancelledWithoutChangingRuntime|TestCompositionRootCacheClear.*|TestApplicationRequestFilterAndDrainDestroyRejectedModelSecrets|Test(ResolvePaths|CanonicalizeExistingHome|SystemPaths).*|TestOpenRespectsExistingUserModesAndConfiguresConnectionPragmas|TestOpenCreatesPrivateStateAndDatabase|TestFileLoggerCreatesOwnerOnlyDirectoryAndFile|TestFileLoggerHandlesUserManagedModesAndRejectsUnsafePaths)$$
MIGRATION_TEST_PATTERN := ^(TestMigrate.*|TestReleasedMigrationMatrix.*|TestApprovalRuntimeMigration.*|TestMinimalRunIdentityMigration.*|TestInitialSchemaContainsOnlyAllowlistedStorageColumns)$$
E2E_TEST_PATTERN := ^(TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis|TestSessionApplicationAdapterUsesRealSQLiteResumeEligibility|TestResumeIntegrationUsesTemporaryDatabaseAndRevalidatesOnlyAcceptedScope|TestDiagnosisScenarioFixtures|TestCoordinator.*(Retention|Privacy|Minimal|Delete|ClearHistory|Export).*|TestApprovalCoordinator.*(SessionDeletion|HistoryDeletion).*|TestCoordinatorRepositoryClearHistory.*|TestDeleteAllLocalState.*|TestExportSummary.*|TestSessionRepositoryExportSnapshot.*|TestExportWriter.*|TestSessionRepositoryDeletionCascades.*|TestRetentionRepository.*|TestPrivacyExport.*|TestPrivacyDoesNotOfferExportForMinimalPersistence|TestPrivacyLifecycleControlsReuseOneComposerAndRequireDeleteConfirmation|TestPrivacyClearHistoryAndDeleteAllRequireExplicitConfirmation|TestResumePickerDeletesOnlyAfterExplicitConfirmation|TestTopLevelResumePickerDeletionNeverFallsBackToANewSession|TestRestartApprovalEndToEndWriteActionMatrix)$$
FUZZ_SEED_PATTERN := ^(FuzzRedactorSafety|FuzzBindToolCallStrictSchema|FuzzBoundedSSEBodyIsChunkIndependent|FuzzParseSlashDraftHasNoDynamicAuthority)$$
IMPORT_GUARD_PATTERN := ^(TestApplicationImportBoundaryIsStatic|TestEinoImportsRemainInTheirSoleTranslationBoundaries|TestExportedKubeBoundaryContainsNoClientGoTypes|TestReadOnlyToolPathContainsNoWriteShellOrGenericKubernetesEscape|TestTUIImportBoundaryAndSingleTextareaAreStatic|TestRepositorySourcesKeepExplicitSQLBoundary|TestSQLXImportRemainsInsideSQLiteAdapter|TestCompositionConstructsOneModelLifecycleAndOneSupervisedRestartPath|TestSelectedDriverAndSQLXContract|TestProductionHasOneClosedSupervisedRestartExecutor)$$

.PHONY: bootstrap-tools release-tools go-version-check release-version-check release-config-check release-cgo-check release-dry-run release-verify fmt imports fmt-check imports-check lint workflow-lint test test-race test-security security test-migration test-e2e test-fuzz-seeds test-performance import-guard dependency-guard vuln vet build cross-build binary-size-check platform-smoke check check-slow check-all

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

$(GORELEASER_BIN):
	mkdir -p "$(dir $@)"
	GOTOOLCHAIN=$(GATE_GO_VERSION) GOBIN="$(abspath $(dir $@))" $(GO_CMD) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

$(SYFT_BIN):
	mkdir -p "$(dir $@)"
	GOTOOLCHAIN=$(GATE_GO_VERSION) GOBIN="$(abspath $(dir $@))" $(GO_CMD) install github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)

bootstrap-tools: $(GOIMPORTS_BIN) $(GOLANGCI_LINT_BIN) $(GOVULNCHECK_BIN) $(ACTIONLINT_BIN)

release-tools: $(GORELEASER_BIN) $(SYFT_BIN)

go-version-check:
	@actual="$$( $(GO_CMD) env GOVERSION )"; \
	if [ "$$actual" != "$(GATE_GO_VERSION)" ]; then \
		printf '%s\n' "CI gates require $(GATE_GO_VERSION); found $$actual."; \
		exit 1; \
	fi

release-version-check:
	@printf '%s\n' "$(RELEASE_VERSION)" | LC_ALL=C grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$' || { \
		printf '%s\n' 'RELEASE_VERSION must be a stable SemVer value without a leading v.'; \
		exit 1; \
	}

release-config-check: go-version-check release-version-check release-tools
	PATH="$(abspath $(dir $(SYFT_BIN))):$$PATH" \
		KUPILOT_RELEASE_VERSION="$(RELEASE_VERSION)" \
		GOTOOLCHAIN=$(GATE_GO_VERSION) \
		"$(GORELEASER_BIN)" check --config .goreleaser.yaml

release-cgo-check: go-version-check
	@set -eu; \
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		goos="$${target%/*}"; \
		goarch="$${target#*/}"; \
		deps="$$(CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" GOTOOLCHAIN=$(GATE_GO_VERSION) $(GO_CMD) list -deps $(COMMAND_PACKAGE))"; \
		printf '%s\n' "$$deps" | grep -qx 'modernc.org/sqlite'; \
		if printf '%s\n' "$$deps" | grep -Eq '^(runtime/cgo|github.com/mattn/go-sqlite3)$$'; then \
			printf '%s\n' "CGO or a CGO SQLite driver entered $$target."; \
			exit 1; \
		fi; \
		cgo_packages="$$(CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" GOTOOLCHAIN=$(GATE_GO_VERSION) $(GO_CMD) list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' $(COMMAND_PACKAGE))"; \
		if [ -n "$$cgo_packages" ]; then \
			printf '%s\n' "CGO files entered $$target:" "$$cgo_packages"; \
			exit 1; \
		fi; \
		printf '%s\n' "Verified CGO-free production dependencies for $$target."; \
	done

release-dry-run: release-config-check release-cgo-check
	@set -eu; \
	release_root="$$(mktemp -d "$${TMPDIR:-/tmp}/kupilot-$(RELEASE_VERSION)-dry-run.XXXXXX")"; \
	goreleaser_dist="$$release_root/work"; \
	release_dist="$$release_root/release"; \
	mkdir "$$goreleaser_dist" "$$release_dist"; \
	printf '%s\n' "Release dry-run workspace: $$release_root"; \
	if [ -e dist ] || [ -L dist ]; then \
		printf '%s\n' 'The repository dist path must not exist before a release dry-run.'; \
		exit 1; \
	fi; \
	ln -s "$$goreleaser_dist" dist; \
	cleanup_release_link() { \
		if [ -L dist ] && [ "$$(readlink dist)" = "$$goreleaser_dist" ]; then \
			rm -f dist; \
		fi; \
	}; \
	trap cleanup_release_link EXIT HUP INT TERM; \
	PATH="$(abspath $(dir $(SYFT_BIN))):$$PATH" \
		KUPILOT_RELEASE_VERSION="$(RELEASE_VERSION)" \
		GOTOOLCHAIN=$(GATE_GO_VERSION) \
		"$(GORELEASER_BIN)" release --snapshot --config .goreleaser.yaml; \
	cleanup_release_link; \
	trap - EXIT HUP INT TERM; \
	for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do \
		cp "$$goreleaser_dist/kupilot_$(RELEASE_VERSION)_$$target.tar.gz" "$$release_dist/"; \
		cp "$$goreleaser_dist/kupilot_$(RELEASE_VERSION)_$$target.tar.gz.spdx.json" "$$release_dist/"; \
	done; \
	cp "$$goreleaser_dist/kupilot_$(RELEASE_VERSION)_checksums.txt" "$$release_dist/"; \
	$(MAKE) --no-print-directory release-verify RELEASE_DIST="$$release_dist" RELEASE_VERSION="$(RELEASE_VERSION)"; \
	printf '%s\n' "Verified candidate assets directory: $$release_dist"

release-verify: release-version-check
	@set -eu; \
	if [ -z "$(RELEASE_DIST)" ]; then \
		printf '%s\n' 'RELEASE_DIST must identify a completed dry-run directory.'; \
		exit 1; \
	fi; \
	dist="$(RELEASE_DIST)"; \
	checksum="$$dist/kupilot_$(RELEASE_VERSION)_checksums.txt"; \
	test -f "$$checksum"; \
	archive_count="$$(find "$$dist" -maxdepth 1 -type f -name 'kupilot_*.tar.gz' | wc -l | tr -d ' ')"; \
	sbom_count="$$(find "$$dist" -maxdepth 1 -type f -name 'kupilot_*.tar.gz.spdx.json' | wc -l | tr -d ' ')"; \
	asset_count="$$(find "$$dist" -maxdepth 1 -type f | wc -l | tr -d ' ')"; \
	entry_count="$$(find "$$dist" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')"; \
	test "$$archive_count" = 4; \
	test "$$sbom_count" = 4; \
	test "$$asset_count" = 9; \
	test "$$entry_count" = 9; \
	if command -v sha256sum >/dev/null 2>&1; then \
		(cd "$$dist" && sha256sum --check "$$(basename "$$checksum")"); \
	else \
		(cd "$$dist" && shasum -a 256 --check "$$(basename "$$checksum")"); \
	fi; \
	scan_root="$$(mktemp -d "$${TMPDIR:-/tmp}/kupilot-release-scan.XXXXXX")"; \
	trap 'rm -rf "$$scan_root"' EXIT HUP INT TERM; \
	host_os="$$(GOTOOLCHAIN=$(GATE_GO_VERSION) $(GO_CMD) env GOHOSTOS)"; \
	host_arch="$$(GOTOOLCHAIN=$(GATE_GO_VERSION) $(GO_CMD) env GOHOSTARCH)"; \
	full_commit="$$(git rev-parse HEAD)"; \
	short_commit="$$(printf '%s' "$$full_commit" | cut -c1-12)"; \
	commit_epoch="$$(git show -s --format=%ct HEAD)"; \
	if release_date="$$(date -u -r "$$commit_epoch" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)"; then \
		:; \
	else \
		release_date="$$(date -u -d "@$$commit_epoch" '+%Y-%m-%dT%H:%M:%SZ')"; \
	fi; \
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		goos="$${target%/*}"; \
		goarch="$${target#*/}"; \
		archive="$$dist/kupilot_$(RELEASE_VERSION)_$${goos}_$${goarch}.tar.gz"; \
		sbom="$$archive.spdx.json"; \
		test -f "$$archive"; \
		test -s "$$sbom"; \
		members="$$(tar -tzf "$$archive" | LC_ALL=C sort)"; \
		expected_members="$$(printf 'LICENSE\nkupilot')"; \
		if [ "$$members" != "$$expected_members" ]; then \
			printf '%s\n' "Unexpected archive members in $$(basename "$$archive"):" "$$members"; \
			exit 1; \
		fi; \
		target_dir="$$scan_root/$${goos}_$${goarch}"; \
		mkdir -p "$$target_dir"; \
		tar -xzf "$$archive" -C "$$target_dir"; \
		test -x "$$target_dir/kupilot"; \
		cmp -s LICENSE "$$target_dir/LICENSE"; \
		metadata="$$target_dir/buildinfo.txt"; \
		GOTOOLCHAIN=$(GATE_GO_VERSION) $(GO_CMD) version -m "$$target_dir/kupilot" > "$$metadata"; \
		head -n 1 "$$metadata" | grep -Fq '$(GATE_GO_VERSION)'; \
		grep -Fq 'CGO_ENABLED=0' "$$metadata"; \
		grep -Fq "GOOS=$$goos" "$$metadata"; \
		grep -Fq "GOARCH=$$goarch" "$$metadata"; \
		grep -Eq 'dep[[:space:]]+modernc\.org/sqlite[[:space:]]+v1\.56\.0' "$$metadata"; \
		if grep -Fq 'github.com/mattn/go-sqlite3' "$$metadata"; then \
			printf '%s\n' "A CGO SQLite dependency entered $$(basename "$$archive")."; \
			exit 1; \
		fi; \
		binary_strings="$$target_dir/strings.txt"; \
		LC_ALL=C strings "$$target_dir/kupilot" > "$$binary_strings"; \
		grep -Fqx "v$(RELEASE_VERSION)" "$$binary_strings"; \
		grep -Fqx "$$full_commit" "$$binary_strings"; \
		grep -Fqx "$$release_date" "$$binary_strings"; \
		if grep -Eq -- '/Users/|/home/|/private/var/|docs/_local|-----BEGIN [A-Z ]*PRIVATE KEY-----|sk-[A-Za-z0-9_-]{24,}|AKIA[0-9A-Z]{16}' "$$binary_strings"; then \
			printf '%s\n' "A prohibited path or secret pattern entered $$(basename "$$archive")."; \
			exit 1; \
		fi; \
		grep -Fq '"spdxVersion":"SPDX-2.3"' "$$sbom"; \
		if grep -Eq -- '/Users/|/home/|/private/var/|docs/_local|-----BEGIN [A-Z ]*PRIVATE KEY-----|sk-[A-Za-z0-9_-]{24,}|AKIA[0-9A-Z]{16}' "$$sbom"; then \
			printf '%s\n' "A prohibited path or secret pattern entered $$(basename "$$sbom")."; \
			exit 1; \
		fi; \
		if [ "$$goos/$$goarch" = "$$host_os/$$host_arch" ]; then \
			version_line="$$("$$target_dir/kupilot" --version)"; \
			expected_version_line="kupilot version=v$(RELEASE_VERSION) commit=$$short_commit built=$$release_date go=$(GATE_GO_VERSION) platform=$$goos/$$goarch"; \
			if [ "$$version_line" != "$$expected_version_line" ]; then \
				printf '%s\n' "Unexpected native version output: $$version_line"; \
				exit 1; \
			fi; \
		fi; \
	done; \
	printf '%s\n' "Verified four archives, checksums, SPDX SBOMs, metadata, and the native --version output."

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

test-performance: build
	$(GO_CMD) test -run '^$$' -bench '^BenchmarkCLIProcessStartupV1$$' -benchtime=1x -count=1 ./cmd/kupilot
	$(GO_CMD) test -run '^$$' -bench '^BenchmarkStreamDeltaMergeV1$$' -benchmem -benchtime=1x -count=1 ./internal/application
	$(GO_CMD) test -run '^$$' -bench '^BenchmarkDiagnosisFixtureMatrixV1$$' -benchmem -benchtime=1x -count=1 ./internal/agent
	$(GO_CMD) test -run '^$$' -bench '^BenchmarkSQLite' -benchmem -benchtime=1x -count=1 ./internal/persistence/sqlite
	$(GO_CMD) test -run '^$$' -bench '^BenchmarkStreamRenderV1' -benchmem -benchtime=1x -count=1 ./internal/tui

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
	CGO_ENABLED=0 $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$(BINARY)" $(COMMAND_PACKAGE)

cross-build:
	mkdir -p "$(CROSS_BIN_DIR)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$(CROSS_BIN_DIR)/kupilot-darwin-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$(CROSS_BIN_DIR)/kupilot-darwin-arm64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$(CROSS_BIN_DIR)/kupilot-linux-amd64" $(COMMAND_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$(CROSS_BIN_DIR)/kupilot-linux-arm64" $(COMMAND_PACKAGE)

binary-size-check: cross-build release-cgo-check
	@set -eu; \
	repeat_root="$$(mktemp -d "$${TMPDIR:-/tmp}/kupilot-size-check.XXXXXX")"; \
	trap 'rm -rf "$$repeat_root"' EXIT HUP INT TERM; \
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		goos="$${target%/*}"; \
		goarch="$${target#*/}"; \
		name="kupilot-$${goos}-$${goarch}"; \
		first="$(CROSS_BIN_DIR)/$$name"; \
		second="$$repeat_root/$$name"; \
		CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" $(GO_CMD) build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w" -o "$$second" $(COMMAND_PACKAGE); \
		first_size="$$(wc -c < "$$first" | tr -d ' ')"; \
		second_size="$$(wc -c < "$$second" | tr -d ' ')"; \
		if [ "$$first_size" != "$$second_size" ]; then \
			printf '%s\n' "Repeated binary size differs for $$target: $$first_size versus $$second_size bytes."; \
			exit 1; \
		fi; \
		if [ "$$first_size" -gt 100663296 ]; then \
			printf '%s\n' "Binary size exceeds the 96 MiB ceiling for $$target: $$first_size bytes."; \
			exit 1; \
		fi; \
		if [ -n "$(BINARY_SIZE_BASELINE_DIR)" ]; then \
			baseline="$(BINARY_SIZE_BASELINE_DIR)/$$name"; \
			if [ ! -f "$$baseline" ]; then \
				printf '%s\n' "Accepted binary baseline is missing for $$target."; \
				exit 1; \
			fi; \
			baseline_size="$$(wc -c < "$$baseline" | tr -d ' ')"; \
			allowed_size="$$(( (baseline_size * 105 + 99) / 100 ))"; \
			if [ "$$first_size" -gt "$$allowed_size" ]; then \
				printf '%s\n' "Binary size exceeds the accepted 5 percent trend budget for $$target: $$first_size versus $$baseline_size bytes."; \
				exit 1; \
			fi; \
		fi; \
		printf '%s\n' "Verified binary size for $$target: $$first_size bytes; repeated build matched."; \
	done

platform-smoke: go-version-check test build

check: go-version-check fmt-check imports-check vet lint workflow-lint test build

check-slow: go-version-check test-race security test-migration test-e2e test-fuzz-seeds test-performance vuln binary-size-check

check-all: check check-slow
