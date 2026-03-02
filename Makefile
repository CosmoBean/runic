.PHONY: build run test clean setup pair dist

VERSION := 0.1.0
BINARY := runic
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) .

run: build
	./$(BINARY) start

setup: build
	./$(BINARY) setup

pair: build
	./$(BINARY) pair

test:
	go test ./... -v

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Cross-compilation
dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64 .
