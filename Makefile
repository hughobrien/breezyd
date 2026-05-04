.PHONY: build test lint tidy clean

build:
	go build -o ./breezyd ./cmd/breezyd
	go build -o ./breezy ./cmd/breezy

test:
	go test -race ./...

lint:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f ./breezy ./breezyd
	go clean -testcache
