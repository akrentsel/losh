.PHONY: build test install

build:
	go build -o bin/losh ./cmd/losh

test:
	go test ./...

install:
	go install ./cmd/losh
