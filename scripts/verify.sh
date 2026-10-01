#!/usr/bin/env bash
# The same verification entrypoint for development, CI and release tags.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
: "${GITMAN_TEST_DATABASE_URL:?Set an isolated database whose name ends in _test}"
[[ -z $(gofmt -l cmd internal) ]] || { gofmt -l cmd internal; exit 1; }
bash -n scripts/*.sh
go vet ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 run --timeout=5m
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go test -race -p 1 -count=1 ./...
mkdir -p bin
go build -trimpath -ldflags "-s -w -X main.version=${VERSION:-dev}" -o bin/gitman ./cmd/gitman
[[ $(bin/gitman version) == "gitman ${VERSION:-dev}" ]]
