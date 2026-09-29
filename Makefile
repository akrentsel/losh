PREFIX ?= /usr/local

.PHONY: build client server-bundles test install

build: client server-bundles

client:
	go build -o bin/losh ./cmd/losh

server-bundles:
	mkdir -p bin/servers/linux-amd64 bin/servers/linux-arm64 bin/servers/darwin-amd64 bin/servers/darwin-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/servers/linux-amd64/losh-server ./cmd/losh
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/servers/linux-arm64/losh-server ./cmd/losh
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/servers/darwin-amd64/losh-server ./cmd/losh
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/servers/darwin-arm64/losh-server ./cmd/losh

test:
	go test ./...
	go vet ./...

install: build
	install -d "$(DESTDIR)$(PREFIX)/bin" "$(DESTDIR)$(PREFIX)/libexec/losh"
	install -m 0755 bin/losh "$(DESTDIR)$(PREFIX)/bin/losh"
	cp -R bin/servers "$(DESTDIR)$(PREFIX)/libexec/losh/servers"
