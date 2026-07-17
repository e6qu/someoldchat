.PHONY: all build build-static build-dqlite test test-race test-dqlite ecs-qualification sdk-qualification browser-qualification compatibility-report contract-ratchet proto-tools generate generate-proto proto-lint generated-check fmt-check contract-check doc-check ci-check sdk-inventory-check check clean run

GOCACHE ?= $(CURDIR)/.cache/go-build
PROTO_BIN ?= $(CURDIR)/.cache/proto-bin

PLAYWRIGHT_INSTALL_FLAGS :=
ifeq ($(shell uname -s),Linux)
PLAYWRIGHT_INSTALL_FLAGS := --with-deps
endif

proto-tools:
	mkdir -p $(PROTO_BIN)
	if test ! -x $(PROTO_BIN)/protoc-gen-go; then GOCACHE=$(GOCACHE) go build -trimpath -o $(PROTO_BIN)/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go; fi
	if test ! -x $(PROTO_BIN)/protoc-gen-go-grpc; then GOBIN=$(PROTO_BIN) GOCACHE=$(GOCACHE) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1; fi

all: check build

build:
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat ./cmd/server
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-chatd ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-activator ./cmd/activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-ecs-ws-activator ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-worker ./cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/sameoldchat-blobgc ./cmd/blobgc

build-static:
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-static ./cmd/server
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-chatd-static ./cmd/chatd
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-activator-static ./cmd/activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-ecs-ws-activator-static ./cmd/ecs-ws-activator
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-worker-static ./cmd/worker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -o bin/sameoldchat-blobgc-static ./cmd/blobgc

build-dqlite:
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-dqlite ./cmd/server
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-chatd-dqlite ./cmd/chatd
	GOCACHE=$(GOCACHE) go build -tags dqlite -trimpath -o bin/sameoldchat-blobgc-dqlite ./cmd/blobgc

test-dqlite:
	GOCACHE=$(GOCACHE) go test -tags dqlite ./...

ecs-qualification:
	PYTHONDONTWRITEBYTECODE=1 python3 tests/ecs-scale-zero-qualification/qualification_test.py

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./...

generate:
	$(MAKE) proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf GOCACHE=$(GOCACHE) go generate ./...

generate-proto: proto-tools
	PATH=$(PROTO_BIN):$(PATH) BUF_CACHE_DIR=$(CURDIR)/.cache/buf buf generate

proto-lint:
	BUF_CACHE_DIR=$(CURDIR)/.cache/buf buf lint

generated-check:
	GOCACHE=$(GOCACHE) go run ./cmd/modulegen -manifest modules.json -out internal/generated/bindings.go -check

fmt-check:
	test -z "$$(gofmt -l .)"

contract-check:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck

doc-check:
	GOCACHE=$(GOCACHE) go run ./cmd/doccheck .

ci-check:
	GOCACHE=$(GOCACHE) go run ./cmd/cicheck .github/workflows/ci.yml

compatibility-report:
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -report

contract-ratchet:
	test -n "$(BASE_REF)"
	GOCACHE=$(GOCACHE) go run ./cmd/contractcheck -ratchet-base "$(BASE_REF)"

sdk-inventory-check:
	GOCACHE=$(GOCACHE) go run ./cmd/sdkcheck -require-qualified

sdk-qualification:
	./tests/official-sdk-qualification/qualify.sh

browser-qualification:
	npm ci --prefix tests/browser
	npx --prefix tests/browser playwright install $(PLAYWRIGHT_INSTALL_FLAGS) --only-shell chromium
	npm test --prefix tests/browser

check: fmt-check contract-check doc-check ci-check sdk-inventory-check proto-lint generated-check test

clean:
	rm -rf bin .cache coverage.out dist deploy/ecs-scale-zero/.terraform

run:
	GOCACHE=$(GOCACHE) go run ./cmd/server -chat-mode local -store memory -api-token xoxb-dev -session-token dev-session
