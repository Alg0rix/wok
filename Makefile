.PHONY: build test vet benchmark

build:
	go build -o bin/wok ./cmd/wok

test:
	go test -race ./...

vet:
	go vet ./...

benchmark: build
	npm ci --prefix bench
	node bench/benchmark.mjs
