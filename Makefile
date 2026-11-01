BIN := bin/couch
E2E_RESULTS := e2e/allure-results

.PHONY: all build build-linux release test vet lint check e2e e2e-report clean

all: lint test build

build:
	cd src && go build -o ../$(BIN) ./cmd/couch

build-linux:
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../$(BIN)-linux-amd64 ./cmd/couch

# The binary for the native install (docs/INSTALL.md), built as in the image.
release:
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o ../$(BIN)-linux-amd64 ./cmd/couch
	cd bin && sha256sum couch-linux-amd64 > couch-linux-amd64.sha256

test:
	cd src && go test -race ./...

vet:
	cd src && go vet ./...

lint:
	cd src && golangci-lint run --config ../.golangci.yml ./...
	cd e2e && golangci-lint run --config ../.golangci.yml ./...

check: build
	node scripts/check-emby-client.mjs

e2e:
	rm -rf $(E2E_RESULTS)
	cd e2e && ALLURE_OUTPUT_PATH=$(CURDIR)/e2e ALLURE_ISSUE_PATTERN=https://github.com/TokenCemetery/couch/issues/%s go test -count=1 -timeout 30m ./...

e2e-report:
	allure generate $(E2E_RESULTS) --output e2e/allure-report

clean:
	rm -f $(BIN) $(BIN)-linux-amd64 $(BIN)-linux-amd64.sha256 coverage.out
	rm -rf $(E2E_RESULTS) e2e/allure-report
