# Contributing to ProGoA2A

Thank you for your interest in contributing to **ProGoA2A**! We welcome contributions from developers, researchers, and AI builders.

---

## Development Setup

### Prerequisites
- **Go**: Version 1.26.6 or newer (as declared in `go.mod`)
- **Make**: For running build and test shortcuts
- **Git**: For version control

### Clone and Build
```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a

# Build the proxy binary
make build

# Run unit and mock tests (skips postgres integration tests when POSTGRES_TEST_DSN is unset)
make test

# Run PostgreSQL integration tests locally:
# 1. Start a disposable database reachable only from the host
docker run --rm -d --name progo-a2a-postgres-test \
  -e POSTGRES_PASSWORD=postgres_test_password \
  -e POSTGRES_DB=progo_test \
  -p 127.0.0.1:5432:5432 postgres:17-alpine

# 2. Run all tests with POSTGRES_TEST_DSN set
POSTGRES_TEST_DSN="postgres://postgres:postgres_test_password@localhost:5432/progo_test?sslmode=disable" go test -count=1 -v -race ./...

# 3. Stop the disposable database
docker stop progo-a2a-postgres-test

# Run throughput benchmarks
make bench
```

---

## Code Quality Standards

1. **Test-Driven Development (TDD) & Integration Testing**:
   - When adding new adapters, features, or storage operations, write unit and integration tests first.
   - All tests must pass with the Go race detector enabled:
     ```bash
     go test -count=1 -v -race ./...
     ```
   - **PostgreSQL Integration Tests**: CI runs integration tests against a `postgres:17-alpine` service container with `POSTGRES_TEST_DSN`. Contributors modifying `pkg/storage` or task persistence must run:
     ```bash
     POSTGRES_TEST_DSN="postgres://postgres:postgres_test_password@localhost:5432/progo_test?sslmode=disable" go test -count=1 -v -race ./pkg/storage/...
     ```
   - Note that task storage is pluggable (`memory` or `postgres`); tests verify in-memory behavior as well as PostgreSQL schema migrations, advisory locking, and atomic upserts.

2. **Clean Go Idioms & Formatting**:
   - Run `go fmt ./...` and `go vet ./...` before submitting PRs.
   - Use standard library constructs (`log/slog`, `net/http`) wherever feasible.

3. **Performance & Memory**:
   - Avoid unbounded memory allocations per request.
   - Benchmark new code paths:
     ```bash
     go test -bench=. -benchmem ./tests/...
     ```

---

## Pull Request Process

1. Fork the repository and create a new feature branch (`git checkout -b feat/my-new-feature`).
2. Make your changes and commit with descriptive commit messages following [Conventional Commits](https://www.conventionalcommits.org/).
3. Ensure all tests and benchmarks pass cleanly.
4. Push your branch to your fork and open a Pull Request against `master`.
5. Clearly describe the problem solved, changes made, and verification evidence in the PR body.

---

## License
By contributing to ProGoA2A, you agree that your contributions will be licensed under the project's [MIT License](LICENSE).
