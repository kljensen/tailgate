# Tailgate justfile

set shell := ["bash", "-cu"]

build:
    go build -mod vendor .

run:
    go run -mod vendor . -verbose

test:
    go test -mod vendor ./...

lint:
    go vet -mod vendor ./...

lint-strict:
    golangci-lint run --timeout=5m ./...

release version:
    CGO_ENABLED=0 go build -ldflags="-X main.version={{version}} -s -w" -o tailgate .

vendor:
    go mod tidy
    go mod vendor
