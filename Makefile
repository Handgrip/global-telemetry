.PHONY: build test lint run clean

VERSION ?= dev

build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/probe-agent ./cmd/probe-agent/

test:
	go test -v -race ./...

lint:
	golangci-lint run ./...

run: build
	sudo ./bin/probe-agent --config configs/agent.example.yaml --debug

clean:
	rm -rf bin/

tidy:
	go mod tidy
