.PHONY: build test bench clean run docker-build

build:
	go build -o bin/progo-a2a ./cmd/proxy

test:
	go test -v -race ./...

bench:
	go test -bench=. -benchmem ./tests/...

clean:
	rm -rf bin/

run:
	go run ./cmd/proxy -config config/progo-a2a.example.yaml

docker-build:
	docker build -t progo-a2a:latest .
