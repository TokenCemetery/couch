BIN := bin/couch
ARCHES := amd64 arm64
E2E_RESULTS := e2e/allure-results
# VERSION (a release tag such as 0.2.0).
VERSION ?=
LDFLAGS := -s -w$(if $(VERSION), -X github.com/TokenCemetery/couch/internal/server.Version=$(VERSION))

.PHONY: all compile build-linux release test lint lint-code lint-docs check e2e e2e-report install-hooks clean

all: lint test compile

compile:
	cd src && go build -o ../$(BIN) ./cmd/couch

build-linux:
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../$(BIN)-linux-amd64 ./cmd/couch

# The binaries for the native install (docs/INSTALL.md), built as in the image.
release: $(ARCHES:%=release-%)

release-%:
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -trimpath -ldflags='$(LDFLAGS)' -o ../$(BIN)-linux-$* ./cmd/couch
	cd bin && sha256sum couch-linux-$* > couch-linux-$*.sha256

test:
	cd src && go test -race ./...

lint: lint-docs lint-code

lint-code:
	cd src && golangci-lint run --config ../.golangci.yml ./...
	cd e2e && golangci-lint run --config ../.golangci.yml ./...

lint-docs:
	npx --yes markdownlint-cli2 "**/*.md" "#node_modules"

check: compile
	node scripts/check-emby-client.mjs

e2e:
	rm -rf $(E2E_RESULTS)
	cd e2e && ALLURE_OUTPUT_PATH=$(CURDIR)/e2e ALLURE_ISSUE_PATTERN=https://github.com/TokenCemetery/couch/issues/%s go test -count=1 -timeout 30m ./...

e2e-report:
	allure generate $(E2E_RESULTS) --output e2e/allure-report

install-hooks:
	git config core.hooksPath .github/hooks

clean:
	rm -f $(BIN) $(BIN)-* coverage.out
	rm -rf $(E2E_RESULTS) e2e/allure-report
