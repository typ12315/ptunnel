VERSION ?= dev

.PHONY: all web build test fmt lint clean

all: web build

web:
	if not exist web\node_modules (cd web && npm install)
	cd web && npm run build

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/ptunnel-server.exe ./cmd/ptunnel-server
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/ptunnel-client.exe ./cmd/ptunnel-client

test:
	go test -race -count=1 ./...

fmt:
	gofmt -w .

lint:
	go vet ./...

clean:
	if exist bin rmdir /s /q bin