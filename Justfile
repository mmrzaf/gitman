set shell := ["bash", "-cu"]


dev:
    go run ./cmd/gitman web

run *args:
    go run ./cmd/gitman {{args}}


build:
    mkdir -p bin
    go build -o bin/gitman ./cmd/gitman

test:
    go test ./...

fmt:
    gofmt -w .

lint:
    golangci-lint run
