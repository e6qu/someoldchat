.PHONY: all build build-static build-dqlite check-dqlite test test-race test-load test-load-race test-transport-load test-mutation test-fuzz test-dqlite test-postgres sdk-qualification external-contract-qualification browser-qualification shauth-sso-qualification compatibility-report contract-ratchet journey-check proto-tools generate generate-proto proto-lint proto-breaking generated-check fmt-check vet vet-dqlite workflow-check container-check module-docs-check module-example-check module-startup-check task-flags-check terraform-check activator-check dependency-check vuln-check vuln-check-dqlite contract-check sdk-inventory-check rebase-audit bench profile check check-full clean run

GOCACHE ?= $(CURDIR)/.cache/go-build
PROTO_BIN ?= $(CURDIR)/.cache/proto-bin
GOWORK := off
export GOWORK
PROTO_GEN_DIR := internal/modules/chat/transport/grpc/gen
# Recursively expanded so `go list` runs only when proto-tools needs it; `:=`
# executed it for every target including `clean`, which then failed or silently
# produced an empty version on a machine without a warm module cache.
PROTOC_GEN_GO_VERSION = $(shell GOCACHE=$(GOCACHE) go list -m -f '{{.Version}}' google.golang.org/protobuf)
PROTOC_GEN_GO_GRPC_VERSION := 1.6.2
# buf is pinned and built here for the same reason as the protoc plugins: it
# selects the lint and breaking-change rule set, so a different buf silently
# changes what `proto-lint` and `proto-breaking` accept. It used to be taken from
# PATH, which meant the local gate and the CI gate could disagree.
BUF_VERSION := 1.71.0
# tests/dependency-admission cross-checks these against
# specs/dependency-admission.yaml, so a pinned tool cannot drift from its
# recorded publication time, checksum, provenance, and license.
GOVULNCHECK_VERSION := 1.6.0

BUF := $(PROTO_BIN)/buf

proto-tools:
	mkdir -p $(PROTO_BIN)
	if test "$$($(PROTO_BIN)/protoc-gen-go --version 2>/dev/null)" != "protoc-gen-go $(PROTOC_GEN_GO_VERSION)"; then GOCACHE=$(GOCACHE) go build -trimpath -o $(PROTO_BIN)/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go; fi
	if test "$$($(PROTO_BIN)/protoc-gen-go-grpc --version 2>/dev/null)" != "protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION)"; then GOBIN=$(PROTO_BIN) GOCACHE=$(GOCACHE) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v$(PROTOC_GEN_GO_GRPC_VERSION); fi
	if test "$$($(BUF) --version 2>/dev/null)" != "$(BUF_VERSION)"; then GOBIN=$(PROTO_BIN) GOCACHE=$(GOCACHE) go install github.com/bufbuild/buf/cmd/buf@v$(BUF_VERSION); fi

all: check build build-static

# The generated composition bindings are an input to every build. Without this
# rule, editing modules.json and running `make build` produced binaries linked
# against the previous bindings.go with no warning, and only `make check` noticed.
internal/generated/bindings.go: modules.json
	GOCACHE=$(GOCACHE) go run ./cmd/modulegen -manifest modules.json -out internal/generated/bindings.go

build: internal/generated/bindings.go
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat ./cmd/server
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-chatd ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-activator ./cmd/activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-ecs-ws-activator ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-worker ./cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-socketmode-worker ./cmd/socketmode-worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-blobgc ./cmd/blobgc

build-static: internal/generated/bindings.go
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-static ./cmd/server
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-chatd-static ./cmd/chatd
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-activator-static ./cmd/activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-ecs-ws-activator-static ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-worker-static ./cmd/worker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-socketmode-worker-static ./cmd/socketmode-worker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-blobgc-static ./cmd/blobgc

build-dqlite: internal/generated/bindings.go
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-dqlite ./cmd/server
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-chatd-dqlite ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-blobgc-dqlite ./cmd/blobgc

test-dqlite:
	GOCACHE=$(GOCACHE) go test -p 1 -tags dqlite ./...

# -p 1 for the same reason test-dqlite uses it: every package here points at the
# one database SAMEOLDCHAT_POSTGRES_DSN names, and Go runs packages in parallel
# by default. Concurrently, tests that migrate a schema, drop a column to prove
# an upgrade path, and quarantine legacy rows are all rewriting the same
# catalogue. That surfaced as three different failures on three different runs —
# a deadlock during CREATE INDEX in CI, and schema assertions failing locally —
# which read like flakiness rather than like one harness fault.
test-postgres:
	@test -n "$(SAMEOLDCHAT_POSTGRES_DSN)" || { echo 'SAMEOLDCHAT_POSTGRES_DSN is required' >&2; exit 1; }
	GOCACHE=$(GOCACHE) go test -p 1 -tags postgres ./tests/persistence-qualification ./internal/store/postgres ./internal/web

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./...

test-load:
	GOCACHE=$(GOCACHE) go test ./tests/load -count=1

# Not part of any gate: the bounded load suite already runs under `check-full`,
# and running it under the race detector is a targeted investigation step for a
# suspected data race in the load paths.
test-load-race:
	GOCACHE=$(GOCACHE) go test -race ./tests/load -count=1

test-transport-load:
	GOCACHE=$(GOCACHE) go test ./internal/modules/chat/transport/grpc -run '^TestRemoteConcurrentPostsPreserveEveryCall$$' -count=1

# Deletes each authorization guard in internal/service in turn and requires a
# suite to notice. Its own target because each guard is a separate compile and
# suite run, and there are hundreds; -timeout is raised for the same reason.
test-mutation:
	SAMEOLDCHAT_MUTATION=1 GOCACHE=$(GOCACHE) go test ./tests/mutation -count=1 -timeout=180m

test-fuzz:
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzListCursorRoundTrips -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzMessageCursorRoundTrips -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzNormalizeScopes -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzNormalizeConversationTypes -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzNormalizeJSONScalarNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzDecodeFieldsNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzNormalizeJSONListFieldNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/store/postgres -run '^$$' -fuzz FuzzRewriteIsIdempotent -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzNormalizeBlocksIsSafeAndIdempotent -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzNormalizeAttachmentsIsSafeAndIdempotent -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzNormalizeUnfurlsIsSafeAndIdempotent -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/lifecycle -run '^$$' -fuzz FuzzManifestDecodingNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/lifecycle -run '^$$' -fuzz FuzzManifestVerificationRejectsForeignSignatures -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/lifecycle -run '^$$' -fuzz FuzzManifestVerificationRejectsTamperedFields -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/lifecycle -run '^$$' -fuzz FuzzManifestVerificationRejectsForeignKeyIDs -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzDecodeListCursorNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzDecodeMessageCursorNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/domain -run '^$$' -fuzz FuzzStoredTimeOrderIsChronological -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzClampLimitNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzParseIDListNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzParseSlackTimestampNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/api/slack -run '^$$' -fuzz FuzzReminderTimeNeverPanics -fuzztime=25000x -parallel=1 -timeout=2m
	GOCACHE=$(GOCACHE) go test ./internal/socketmode -run '^$$' -fuzz FuzzEncodeEventMatchesDeliverability -fuzztime=25000x -parallel=1 -timeout=2m

generate:
	$(MAKE) proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf GOCACHE=$(GOCACHE) go generate ./...

generate-proto: proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf $(BUF) generate

proto-lint: proto-tools
	BUF_CACHE_DIR=$(CURDIR)/.cache/buf $(BUF) lint

# The http and chat processes of the distributed targets run at independent
# replica counts (modules.json), so every rollout serves mixed versions of the
# gRPC contract and wire skew is guaranteed, not hypothetical. buf.yaml selects
# WIRE_JSON, which rejects the changes that corrupt requests across that skew
# while still allowing additive evolution. Nothing enforced it: `proto-lint`
# checks style, and cmd/contractcheck ratchets the Slack HTTP ledger, not the
# protobuf wire format.
#
# BASE_REF follows the contract-ratchet convention so one workflow expression
# drives both gates.
proto-breaking: proto-tools
	@test -n "$(BASE_REF)" || { echo 'BASE_REF is required' >&2; exit 1; }
	BUF_CACHE_DIR=$(CURDIR)/.cache/buf $(BUF) breaking --against ".git#ref=$(BASE_REF)"

# Regenerates into a throwaway directory and compares recursively, so the check
# is read-only and detects a file the generator would add as well as generated
# output orphaned by a deleted proto. `git diff` saw neither: it ignores
# untracked files and it reported a clean tree while the generator was writing
# into the working copy.
generated-check: proto-tools
	GOCACHE=$(GOCACHE) go run ./cmd/modulegen -manifest modules.json -out internal/generated/bindings.go -check
	@set -eu; \
	scratch="$$(mktemp -d)"; \
	trap 'rm -rf "$$scratch"' EXIT INT TERM; \
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf $(BUF) generate -o "$$scratch"; \
	if ! diff -ru "$$scratch/$(PROTO_GEN_DIR)" "$(PROTO_GEN_DIR)"; then \
		echo 'generated protobuf output does not match $(PROTO_GEN_DIR); run make generate-proto' >&2; \
		exit 1; \
	fi

# `gofmt -l` reports formatting differences on stdout but writes parse errors to
# stderr and exits 2, so the previous `test -z "$(... | xargs gofmt -l)"` passed
# on Go that does not even parse: the substitution captured stdout only and the
# pipeline's exit status was discarded. Both streams are captured and both the
# report and the exit status are checked. `-z`/`xargs -0` keeps paths containing
# spaces intact.
fmt-check:
	@set -eu; \
	report="$$(mktemp)"; \
	trap 'rm -f "$$report"' EXIT INT TERM; \
	if ! git ls-files -z -- '*.go' | xargs -0 gofmt -l -e >"$$report" 2>&1; then \
		echo 'gofmt failed:' >&2; \
		cat "$$report" >&2; \
		exit 1; \
	fi; \
	if test -s "$$report"; then \
		echo 'gofmt reported unformatted or unparsable files:' >&2; \
		cat "$$report" >&2; \
		exit 1; \
	fi

# Build-tagged code is analysed too. `go vet ./...` analyses one build
# configuration, so internal/store/postgres's tagged tests and internal/web's
# postgres-tagged tests were analysed by nothing at all while CI built and ran
# them: a vet-class defect there shipped with every gate green. The dqlite
# variant needs libdqlite headers, which only the CI dqlite job has, so it is a
# separate target that job runs.
vet:
	GOCACHE=$(GOCACHE) go vet ./...
	GOCACHE=$(GOCACHE) go vet -tags postgres ./...

vet-dqlite:
	GOCACHE=$(GOCACHE) go vet -tags dqlite ./...

# specs/dependency-policy.md requires govulncheck for Go source as a mandatory
# control on every dependency-changing change; no workflow ran it. The tag
# blind spot is the same one `vet` had: github.com/canonical/go-dqlite is reached
# only under the dqlite tag, so the default scan never sees it.
vuln-check:
	GOCACHE=$(GOCACHE) go run golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION) ./...
	GOCACHE=$(GOCACHE) go run golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION) -tags postgres ./...

vuln-check-dqlite:
	GOCACHE=$(GOCACHE) go run golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION) -tags dqlite ./...

# Pin syntax for workflow actions, container images, apt packages, Terraform
# versions and providers, and pinned generators is enforced by the
# dependency-admission inventory verifier, which is what specs/dependency-policy.md
# attributes to this target. It previously had no recipe at all and was a bare
# alias, so the rules the policy described existed nowhere.
workflow-check:
	GOCACHE=$(GOCACHE) go run ./tests/dependency-admission

container-check: dependency-check
	./scripts/check-container-publication.sh

# Terraform validate never reads a README and terraform plan needs credentials,
# so a module example that omits a required variable — or passes one the module
# does not declare — was invisible to every gate.
module-docs-check:
	./scripts/check-terraform-module-docs.sh

# terraform/ecs-runtime exported an environment and a secret set cmd/server
# refuses, so following its README produced a crash-looping service with every
# gate green: nothing ever started the binary against a module's own output.
module-startup-check:
	GOCACHE=$(GOCACHE) ./scripts/check-terraform-module-startup.sh

# `terraform validate` treats a container command as an opaque list of strings and
# no Go test reads a .tf file, so deploy/ecs-scale-zero passed three flags
# cmd/ecs-ws-activator had stopped accepting and omitted three it requires. The
# deployed task would have exited 2 on every start with every gate green.
task-flags-check:
	GOCACHE=$(GOCACHE) ./scripts/check-task-definition-flags.sh

dependency-check:
	GOTOOLCHAIN=local GOCACHE=$(GOCACHE) go list -mod=readonly all >/dev/null
	GOCACHE=$(GOCACHE) go mod verify
	GOCACHE=$(GOCACHE) go run ./tests/dependency-admission

contract-check:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck

journey-check:
	GOCACHE=$(GOCACHE) go run ./cmd/journeycheck

compatibility-report:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -report

contract-ratchet:
	@test -n "$(BASE_REF)" || { echo 'BASE_REF is required' >&2; exit 1; }
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -ratchet-base "$(BASE_REF)"

rebase-audit:
	@test -n "$(PARENT)" || { echo 'PARENT is required' >&2; exit 1; }
	@test -n "$(BRANCH)" || { echo 'BRANCH is required' >&2; exit 1; }
	GOCACHE=$(GOCACHE) go run ./cmd/rebaseaudit -parent "$(PARENT)" -branch "$(BRANCH)" -target "$(or $(TARGET),HEAD)"

BENCH ?= .
BENCH_PKG ?= ./internal/domain ./internal/store/sqlstore ./tests/load
BENCHTIME ?= 1s
PROFILE_DIR ?= $(CURDIR)/.cache/profiles

bench:
	GOCACHE=$(GOCACHE) go test $(BENCH_PKG) -run '^$$' -bench '$(BENCH)' -benchtime=$(BENCHTIME) -benchmem

# Writes CPU and allocation profiles for one package so a regression can be
# attributed to a call site instead of guessed at. PKG must name a single
# package; profiles from several packages would overwrite each other.
PROFILE_PKG ?= ./internal/domain

profile:
	mkdir -p $(PROFILE_DIR)
	GOCACHE=$(GOCACHE) go test $(PROFILE_PKG) -run '^$$' -bench '$(BENCH)' -benchtime=$(BENCHTIME) -benchmem \
		-cpuprofile $(PROFILE_DIR)/cpu.out -memprofile $(PROFILE_DIR)/mem.out -o $(PROFILE_DIR)/bench.test
	@echo
	@echo "profiles written to $(PROFILE_DIR)"
	@echo "  go tool pprof -top -nodecount=20 $(PROFILE_DIR)/bench.test $(PROFILE_DIR)/cpu.out"
	@echo "  go tool pprof -top -nodecount=20 -sample_index=alloc_space $(PROFILE_DIR)/bench.test $(PROFILE_DIR)/mem.out"

sdk-inventory-check:
	GOCACHE=$(GOCACHE) go run ./cmd/sdkcheck -require-qualified

sdk-qualification:
	./tests/official-sdk-qualification/qualify.sh

external-contract-qualification:
	./tests/external-contract-qualification/qualify.sh

browser-qualification:
	npm ci --prefix tests/browser
	npx --prefix tests/browser playwright install --with-deps chromium firefox webkit
	npm test --prefix tests/browser

shauth-sso-qualification:
	@test -n "$(SHAUTH_SOURCE_DIR)" || { echo 'SHAUTH_SOURCE_DIR is required' >&2; exit 1; }
	./scripts/test-shauth-sso.sh

# The Terraform job of the pull-request workflow, reachable from `make`. Neither
# `check` nor `check-full` reached it, so a commit that left a module
# `terraform fmt`-dirty or invalid passed every local gate and failed CI. It
# needs the provider registry for `terraform init`, which is why it is not in
# `check`.
terraform-check: module-example-check
	@set -eu; \
	for directory in deploy/ecs-scale-zero terraform/ecs-runtime; do \
		( cd "$$directory" && terraform fmt -check -recursive && terraform init -backend=false -input=false >/dev/null && terraform validate ); \
	done

# module-docs-check compares argument names, which is all a name comparison can
# decide: it never evaluated a module's own `validation` blocks and never checked
# a type, so a README could document the one call the module exists to forbid.
# This runs the example through terraform itself, which is why it lives here and
# not in `check`: it needs the provider registry.
module-example-check:
	./scripts/check-terraform-module-examples.sh

# The activator Lambda is shipped verbatim by deploy/ecs-scale-zero and is the
# public front door of that profile. No make target ran its tests at all.
activator-check:
	python3 -m compileall -q deploy/ecs-scale-zero/activator.py
	python3 deploy/ecs-scale-zero/activator_test.py

# The dqlite build configuration reached by no local gate at all: `vet` and
# `vuln-check` analyse the default and postgres configurations, so a vet-class
# or vulnerability-class defect in internal/store/dqlite was invisible to
# `check` and to `check-full` alike, and visible only inside the CI dqlite job.
# It needs the libdqlite headers, which is why it is its own target rather than
# part of `check`; the CI job runs the same four targets, and the publication
# contract requires that job to run them.
check-dqlite: build-dqlite vet-dqlite vuln-check-dqlite test-dqlite

check: fmt-check vet workflow-check container-check module-docs-check module-startup-check task-flags-check dependency-check contract-check journey-check sdk-inventory-check proto-lint generated-check activator-check test

# Everything the pull-request workflow gates on that does not need a service, a
# second language runtime, or a container build: the `go`, `terraform`, and
# `scale-zero-artifacts` jobs. It deliberately does not reach the `sdk`,
# `browser`, `shauth-sso`, `dqlite`, or `postgres` jobs, or the dual-architecture
# edge image build in `scale-zero-artifacts`, each of which needs something this
# target cannot assume is installed. It used to claim it covered everything,
# while reaching neither `terraform fmt` nor the activator tests. `check-dqlite`
# is the fifth: run it on a machine that has the libdqlite headers.
check-full: check terraform-check vuln-check test-race test-load test-transport-load test-mutation test-fuzz build-static

clean:
	rm -rf bin .cache coverage.out dist deploy/ecs-scale-zero/.terraform terraform/ecs-runtime/.terraform \
		deploy/ecs-scale-zero/.activator.zip deploy/ecs-scale-zero/__pycache__

run:
	GOCACHE=$(GOCACHE) go run ./cmd/server -chat-mode local -store memory -api-token xoxb-dev -session-token dev-session
