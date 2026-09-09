# Contributing to ProGoA2A

Thank you for your interest in contributing to **ProGoA2A**! We welcome contributions from developers, researchers, and AI builders.

---

## Development Setup

### Prerequisites
- **Go**: Version 1.22+ (Go 1.26+ recommended)
- **Make**: For running build and test shortcuts
- **Git**: For version control

### Clone and Build
```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a

# Build the proxy binary
make build

# Run unit and integration tests
make test

# Run throughput benchmarks
make bench
```

---

## Code Quality Standards

1. **Test-Driven Development (TDD)**:
   - When adding new adapters or features, write unit tests first.
   - All tests must pass with the Go race detector enabled:
     ```bash
     go test -count=1 -v -race ./...
     ```

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
