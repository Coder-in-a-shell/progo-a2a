# Installation

The repository currently publishes source code, not release binaries or a container package. Build locally using Go or Docker.

## Build from source

Requirements: Git, Make, and the Go version declared in `go.mod` (currently Go 1.26.6 or newer).

```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a
make build
./bin/progo-a2a -h
```

Supported flags:

| Flag | Default | Purpose |
|---|---|---|
| `-config` | `config/progo-a2a.example.yaml` | YAML configuration path |
| `-host` | config value | Override the configured listen host |
| `-port` | config value | Override the configured listen port |
| `-log-level` | `info` | `debug`, `info`, `warn`, or `error` |

Useful Make targets:

```bash
make build         # bin/progo-a2a
make test          # race-enabled test suite
make bench         # local microbenchmarks
make run           # run with the checked-in example config
make docker-build  # local image progo-a2a:latest
make clean
```

## Run with Docker Compose

The checked-in Compose file builds the image from the local checkout:

```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a
docker compose up --build
```

It mounts `config/progo-a2a.example.yaml` at `/app/config/progo-a2a.yaml`. The sample upstream endpoints are placeholders, so health checks work but agent calls require configuration changes.

## Build and run the container directly

```bash
docker build -t progo-a2a:local .
docker run --rm -p 8080:8080 \
  -v "$PWD/config/progo-a2a.example.yaml:/app/config/progo-a2a.yaml:ro" \
  progo-a2a:local
```

To use your own config, replace the host-side path before the colon.

## Verify the service

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

Typical bodies:

```json
{"status":"healthy","time":"2026-09-10T00:00:00Z"}
```

```json
{"status":"ready","adapters":5,"time":"2026-09-10T00:00:00Z"}
```

`/readyz` confirms that configuration exists and at least one adapter is registered; it does not probe every upstream agent.

## Next

[Make the first agent call](quickstart.md){ .md-button .md-button--primary }
