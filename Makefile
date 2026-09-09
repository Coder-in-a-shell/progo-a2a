.PHONY: build test bench clean run

build:
	go build -o bin/a2a-proxy ./cmd/proxy

test:
	go test -v -race ./...

bench:
	go test -bench=. -benchmem ./tests/...

clean:
	rm -rf bin/

run:
	go run ./cmd/proxy -config config/a2a-proxy.example.yaml
