.PHONY: build test bench clean run docker-build docker-up docker-down

build:
	go build -o bin/progo-a2a ./cmd/proxy

test:
	go test -v -race ./...

bench:
	go test -bench=. -benchmem ./tests/...

clean:
	rm -rf bin/

run:
	STORAGE_BACKEND=memory \
	ADMIN_API_KEY=admin-local-key \
	ANALYST_API_KEY=analyst-local-key \
	LANGGRAPH_API_KEY=local-langgraph-key \
	CREWAI_API_TOKEN=local-crewai-token \
	AUTOGEN_API_KEY=local-autogen-key \
	OPENAI_API_KEY=local-openai-key \
	ENTERPRISE_AUTH_TOKEN=local-enterprise-token \
	go run ./cmd/proxy -config config/progo-a2a.example.yaml

docker-build:
	docker build -t progo-a2a:latest .

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down
