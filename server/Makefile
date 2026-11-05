.PHONY: build test lint fmt

build:
	go build ./...

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

fmt:
	gofmt -w .
