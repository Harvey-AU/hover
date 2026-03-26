# Development Documentation

Setup guides, workflow processes, and debugging tools for Hover development.

## 📄 Documents

### Setup & Workflow

- **[DEVELOPMENT.md](./DEVELOPMENT.md)** - Local development setup and
  contributing guidelines
- **[BRANCHING.md](./BRANCHING.md)** - Git workflow, branch naming, and PR
  process
- **[OPENCODE_DESKTOP.md](./OPENCODE_DESKTOP.md)** - OpenCode Desktop project
  configuration for MCP, LSP, and plugins

### Debugging & Performance

- **[flight-recorder.md](./flight-recorder.md)** - Performance profiling with
  Go's flight recorder

## 🚀 Quick Start

1. **Environment Setup** - Follow [DEVELOPMENT.md](./DEVELOPMENT.md) to
   configure your local environment
2. **Database Setup** - Set up PostgreSQL and run migrations
3. **Run Locally** - `go run ./cmd/app/main.go`
4. **Run Tests** - `go test ./...`

## 🔧 Development Workflow

1. Create feature branch from `main`
2. Develop and test locally
3. Push to feature branch
4. Create PR to `test-branch` for testing
5. After approval, merge to `main`

See [BRANCHING.md](./BRANCHING.md) for detailed Git workflow.

## 🔗 Related Documentation

- [Architecture](../architecture/) - System design and components
- [Testing Guide](../testing/) - How to write and run tests
- [API Reference](../architecture/API.md) - Endpoint documentation
