.PHONY: all build build-static build-dqlite test test-race test-load test-load-race test-transport-load test-fuzz test-dqlite test-postgres sdk-qualification browser-qualification shauth-sso-qualification compatibility-report contract-ratchet proto-tools generate generate-proto proto-lint generated-check fmt-check workflow-check container-check dependency-check contract-check sdk-inventory-check rebase-audit bench profile check clean run

GOCACHE ?= $(CURDIR)/.cache/go-build
PROTO_BIN ?= $(CURDIR)/.cache/proto-bin
GOWORK := off
export GOWORK
PROTOC_GEN_GO_VERSION := $(shell go list -m -f '{{.Version}}' google.golang.org/protobuf)

proto-tools:
	mkdir -p $(PROTO_BIN)
	if test "$$($(PROTO_BIN)/protoc-gen-go --version 2>/dev/null)" != "protoc-gen-go $(PROTOC_GEN_GO_VERSION)"; then GOCACHE=$(GOCACHE) go build -trimpath -o $(PROTO_BIN)/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go; fi
	if test "$$($(PROTO_BIN)/protoc-gen-go-grpc --version 2>/dev/null)" != "protoc-gen-go-grpc 1.6.2"; then GOBIN=$(PROTO_BIN) GOCACHE=$(GOCACHE) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2; fi

all: check build

build:
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat ./cmd/server
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-chatd ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-activator ./cmd/activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-ecs-ws-activator ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-worker ./cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-socketmode-worker ./cmd/socketmode-worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-blobgc ./cmd/blobgc

build-static:
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-static ./cmd/server
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-chatd-static ./cmd/chatd
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-activator-static ./cmd/activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-ecs-ws-activator-static ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-worker-static ./cmd/worker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-socketmode-worker-static ./cmd/socketmode-worker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-blobgc-static ./cmd/blobgc

build-dqlite:
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-dqlite ./cmd/server
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-chatd-dqlite ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-blobgc-dqlite ./cmd/blobgc

test-dqlite:
	GOCACHE=$(GOCACHE) go test -p 1 -tags dqlite ./...

test-postgres:
	test -n "$(SAMEOLDCHAT_POSTGRES_DSN)"
	GOCACHE=$(GOCACHE) go test -tags postgres ./tests/persistence-qualification ./internal/store/postgres ./internal/web

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./...

test-load:
	GOCACHE=$(GOCACHE) go test ./tests/load -count=1

test-load-race:
	GOCACHE=$(GOCACHE) go test -race ./tests/load -count=1

test-transport-load:
	GOCACHE=$(GOCACHE) go test ./internal/modules/chat/transport/grpc -run '^TestRemoteConcurrentPostsPreserveEveryCall$$' -count=1

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

generate:
	$(MAKE) proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf GOCACHE=$(GOCACHE) go generate ./...

generate-proto: proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf buf generate

proto-lint:
	BUF_CACHE_DIR=$(CURDIR)/.cache/buf buf lint

generated-check:
	GOCACHE=$(GOCACHE) go run ./cmd/modulegen -manifest modules.json -out internal/generated/bindings.go -check
	$(MAKE) generate-proto
	git diff --exit-code -- internal/modules/chat/transport/grpc/gen

fmt-check:
	test -z "$$(git ls-files -- '*.go' | xargs gofmt -l)"

workflow-check: dependency-check

container-check: dependency-check
	./scripts/check-container-publication.sh

dependency-check:
	GOTOOLCHAIN=local GOCACHE=$(GOCACHE) go list -mod=readonly all >/dev/null
	GOCACHE=$(GOCACHE) go mod verify
	GOCACHE=$(GOCACHE) go run ./tests/dependency-admission

contract-check:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck

compatibility-report:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -report

contract-ratchet:
	test -n "$(BASE_REF)"
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -ratchet-base "$(BASE_REF)"

rebase-audit:
	test -n "$(PARENT)"
	test -n "$(BRANCH)"
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

browser-qualification:
	npm ci --prefix tests/browser
	npx --prefix tests/browser playwright install --with-deps chromium
	npm test --prefix tests/browser

shauth-sso-qualification:
	test -n "$(SHAUTH_SOURCE_DIR)"
	./scripts/test-shauth-sso.sh

check: fmt-check workflow-check container-check dependency-check contract-check sdk-inventory-check proto-lint generated-check test

clean:
	rm -rf bin .cache coverage.out dist deploy/ecs-scale-zero/.terraform terraform/ecs-runtime/.terraform

run:
	GOCACHE=$(GOCACHE) go run ./cmd/server -chat-mode local -store memory -api-token xoxb-dev -session-token dev-session
