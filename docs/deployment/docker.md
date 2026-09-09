# Docker and Compose

The repository contains a multi-stage Dockerfile and a local-development Compose file. It does not currently publish an official image to a registry.

## Build the image

```bash
docker build -t progo-a2a:local .
```

The builder uses Go 1.26.6 on Alpine and produces a static Linux binary. The runtime image is Alpine with CA certificates and timezone data. It is not a `scratch` image, so the included `wget` supports the container health check.

## Run with your config

```bash
docker run --rm \
  --name progo-a2a \
  -p 8080:8080 \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -v "$PWD/progo-a2a.yaml:/app/config/progo-a2a.yaml:ro" \
  progo-a2a:local
```

The image entrypoint is `/app/progo-a2a`; its default command is:

```text
-config /app/config/progo-a2a.yaml
```

Append CLI flags to override that command:

```bash
docker run --rm progo-a2a:local \
  -config /app/config/progo-a2a.yaml \
  -log-level warn
```

## Docker Compose

The checked-in `docker-compose.yml` builds from the checkout and mounts the example config:

```bash
docker compose up --build -d
docker compose ps
docker compose logs -f progo-a2a
```

Before real agent calls, copy and edit the example, then change the Compose volume source to your file:

```yaml
services:
  progo-a2a:
    build: .
    ports:
      - "8080:8080"
    environment:
      OPENAI_API_KEY: "${OPENAI_API_KEY}"
    volumes:
      - ./progo-a2a.yaml:/app/config/progo-a2a.yaml:ro
```

Environment variables are used only where the YAML includes `${NAME}` placeholders. There are no special runtime variables such as `PROGO_PORT` or `PROGO_LOG_LEVEL`; use CLI flags and YAML server settings.

## Verify

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

## Production image guidance

- Publish to a registry you control and pin an immutable tag or digest.
- Scan both Go dependencies and the final image.
- Keep the configured non-root `progo-a2a` user when extending the image.
- Mount configuration read-only and inject secrets through your platform.
- Put TLS, request-rate controls, and public-edge protections in front of the service.
- Set a container stop-grace period that matches the application's fixed 15-second graceful-shutdown window.
