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

The checked-in `docker-compose.yml` provides a complete local development environment comprising the ProGoA2A gateway and a PostgreSQL 17 Alpine database service:

```bash
# Start the stack (builds gateway, starts PostgreSQL and runs migrations)
make docker-up
# Or directly with Docker Compose:
docker compose up -d --build

# View running containers and logs
docker compose ps
docker compose logs -f progo-a2a

# Stop the stack safely (preserves the named database volume postgres_data)
make docker-down
# Or directly:
docker compose down
```

The default stack configures:

- **PostgreSQL 17 Alpine**: Uses named volume `postgres_data` and a `pg_isready` health check. The database port (`5432`) is restricted to the internal Docker network and not published to the host.
- **Gateway Dependency**: `progo-a2a` depends on database health (`condition: service_healthy`).
- **Storage & Migrations**: Configured with `STORAGE_BACKEND=postgres`, `DATABASE_URL`, and `MIGRATE_ON_START=true`, which applies embedded migrations on startup using advisory locking.
- **Credential Placeholders**: Supplies local dummy keys for inbound authentication and all five adapters (`LANGGRAPH_API_KEY`, `CREWAI_API_TOKEN`, `AUTOGEN_API_KEY`, `OPENAI_API_KEY`, `ENTERPRISE_AUTH_TOKEN`).
- **Overridable Passwords**: Host environment variables or `.env` can override `POSTGRES_PASSWORD` and other settings.

See the [PostgreSQL storage guide](postgresql.md) for schema details, pool tuning, and production recommendations.

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
