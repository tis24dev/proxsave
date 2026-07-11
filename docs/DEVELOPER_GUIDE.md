# Developer Guide

Guide for contributing to Proxsave, including development setup, coding guidelines, and useful commands.

## Table of Contents

- [Overview](#overview)
- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Building & Running](#building--running)
- [Testing](#testing)
- [Dependency Management](#dependency-management)
- [Code Guidelines](#code-guidelines)
- [Contributing](#contributing)
- [Useful Commands](#useful-commands)
- [Related Documentation](#related-documentation)

---

## Overview

Proxsave is built with modern Go practices, emphasizing:

- **Performance**: Compiled binary, concurrent operations
- **Reliability**: Comprehensive error handling, safe defaults
- **Maintainability**: Clean architecture, modular design
- **Testability**: Unit tests, integration tests, mocking

**Technology stack**:
- **Language**: Go 1.25+
- **Dependencies**: See `go.mod` for complete list
- **Build system**: Makefile + Go modules
- **Compression**: gzip, bzip2, xz, lzma, zstd
- **Encryption**: AGE (age-encryption.org)
- **Cloud storage**: rclone integration

---

## Development Setup

### Prerequisites

```bash
# Go 1.25 or later
go version

# Build tools
make --version

# Optional: rclone for cloud storage development
rclone version

# Optional: age for encryption development
age --version
```

### Clone Repository

```bash
# Clone from GitHub
git clone https://github.com/tis24dev/proxsave.git
cd proxsave

# Install dependencies
go mod tidy

# Build
make build

# Run tests
go test ./...
```

### Development Environment

**Recommended IDE setup**:
- **VSCode** with Go extension
- **GoLand** by JetBrains
- **Vim/Neovim** with vim-go

**VSCode settings** (`.vscode/settings.json`):
```json
{
  "go.useLanguageServer": true,
  "go.lintTool": "golangci-lint",
  "go.lintOnSave": "workspace",
  "go.formatTool": "goimports",
  "editor.formatOnSave": true,
  "go.testFlags": ["-v"],
  "go.coverOnSave": true
}
```

---

## Project Structure

```
proxsave/
├── cmd/
│   └── proxsave/              # Main entry point, CLI commands, and the resident daemon (daemon*.go)
├── internal/                  # Private application code
│   ├── backup/                # Collector recipes/bricks, archiving, compression, manifests, checksums
│   ├── checks/                # Dependency and system checks
│   ├── cli/                   # CLI argument parsing (stdlib flag)
│   ├── closeerr/              # Deferred-close error helpers
│   ├── config/                # Configuration management and embedded template
│   ├── cron/                  # Cron scheduler integration
│   ├── environment/           # PVE/PBS environment detection
│   ├── health/                # Healthchecks monitoring and daemon reporting
│   ├── identity/              # Server identity and relay-secret provisioning
│   ├── input/                 # Interactive input helpers
│   ├── installer/             # Install and upgrade flows
│   ├── lint/                  # Internal static-analysis helpers
│   ├── logging/               # Logging and secret redaction
│   ├── metrics/               # Prometheus metrics export
│   ├── notify/                # Notification channels (Telegram/Email/Gotify/Webhook)
│   ├── orchestrator/          # Backup and restore workflows (the restore engine lives here)
│   ├── pbs/                   # PBS helpers
│   ├── safeexec/              # Guarded external-command execution
│   ├── safefs/                # Filesystem-safety guards (I/O timeouts, mount checks)
│   ├── security/              # Security checks and permissions
│   ├── serverbot/             # Leaf transport to the centralized bot-server
│   ├── storage/               # Storage backends (local/secondary/cloud)
│   ├── support/               # Support-bundle helpers
│   ├── testutil/              # Shared test utilities
│   ├── types/                 # Shared types
│   ├── ui/                    # Charm (bubbletea) TUI: dashboard, wizards, forms
│   ├── uitest/                # Race-aware UI test helpers
│   └── version/               # Version info
├── pkg/                       # Shared helper packages (bech32, utils); not an implicit stable external API
├── build/                     # Build artifacts (binary output)
├── docs/                      # Documentation
├── go.mod                     # Go module definition
├── go.sum                     # Dependency checksums
├── Makefile                   # Build automation
└── README.md                  # Minimal root readme (see docs/ for the full set)
```

The live `backup.env` is not in the source tree. The configuration template is
embedded in the binary (`internal/config`) and the working config is created under
the install base dir at install time (see [INSTALL.md](INSTALL.md)). The UI stack is
Charm (`charm.land/bubbletea/v2` and friends); the legacy `tview`/`tcell` stack was
removed and a `make lint` guard (`check-no-tview`) keeps it out.

### Key Modules

| Module | Purpose | Files |
|--------|---------|-------|
| **orchestrator** | Core backup/restore orchestration and capability-based restore decisions | `internal/orchestrator/*.go` |
| **config** | Configuration management | `internal/config/config.go` |
| **storage** | Local/secondary/cloud storage | `internal/storage/*.go` |
| **backup** | Collector recipes/bricks, archiving, compression, manifest/checksum helpers | `internal/backup/*.go` |
| **notify** | Notification channels (see [NOTIFICATIONS.md](NOTIFICATIONS.md)) | `internal/notify/*.go` |
| **serverbot** | Leaf transport to the centralized bot-server (see [NOTIFICATIONS.md](NOTIFICATIONS.md)) | `internal/serverbot/*.go` |
| **health** | Healthchecks monitoring; the resident daemon lives in `cmd/proxsave/daemon*.go` (see [DAEMON.md](DAEMON.md)) | `internal/health/*.go` |
| **ui** | Charm (bubbletea) TUI: dashboard, wizards, forms (see [DASHBOARD_TUI.md](DASHBOARD_TUI.md)) | `internal/ui/*.go` |
| **security** | Security checks, permissions | `internal/security/*.go` |

---

### Collector Architecture

The backup collector is no longer organized around large branch-specific
wrappers. It is built from explicit recipes and fine-grained bricks:

- `newPVERecipe()`
- `newPBSRecipe()`
- `newDualRecipe()`
- `newSystemRecipe()`

Important invariants:

- `dual` is a real type, not an alias
- `dual` composes PVE + PBS bricks in a single run
- `system/common` runs only once
- `storage_stack` belongs to `common/system`, not PBS

For the authoritative architecture description, see
[Collector Architecture](COLLECTOR_ARCHITECTURE.md).

---

## Building & Running

### Development Build

```bash
# Standard development build
make build

# Output: build/proxsave
```

### Optimized Build

```bash
# Optimized build (stripped symbols, smaller binary)
go build -ldflags="-s -w" -o build/proxsave ./cmd/proxsave

# With version info
VERSION=$(git describe --tags --always)
go build -ldflags="-s -w -X main.version=${VERSION}" -o build/proxsave ./cmd/proxsave
```

### Run Without Building

```bash
# Run directly with go run
make run

# Or manually
go run ./cmd/proxsave
```

### Clean Build Artifacts

```bash
# Remove build directory
make clean

# Full clean (including dependencies cache)
go clean -cache -modcache
make build
```

### Cross-Compilation

```bash
# Linux AMD64 (the only published target)
GOOS=linux GOARCH=amd64 go build -o build/proxsave-linux-amd64 ./cmd/proxsave
```

---

## Testing

### Run All Tests

```bash
# All tests
go test ./...

# With coverage
go test -cover ./...

# With coverage report
make test-coverage
# Output: coverage.html (open in browser)
```

### Run Specific Tests

```bash
# Specific package
go test ./internal/config

# Specific test function
go test ./internal/config -run TestLoadConfig

# Verbose output
go test -v ./...
```

### Coverage Analysis

```bash
# Generate coverage profile
go test -coverprofile=coverage.out ./...

# View coverage in terminal
go tool cover -func=coverage.out

# Generate HTML report
go tool cover -html=coverage.out -o coverage.html
```

The Makefile wraps these with a pinned toolchain:

```bash
# Coverage profile + HTML (opens coverage.out)
make test-coverage

# Full coverage report across all packages (prints the total)
make coverage

# Enforce a minimum total coverage (default 50%, override COVERAGE_THRESHOLD)
make coverage-check
make coverage-check COVERAGE_THRESHOLD=60.0
```

### Benchmark Tests

```bash
# Run benchmarks
go test -bench=. ./...

# Benchmark with memory stats
go test -bench=. -benchmem ./...

# Benchmark a specific package (compression lives in internal/backup)
go test -bench=. ./internal/backup
```

---

## Dependency Management

The CLI is built on the standard library `flag` package, not a third-party
framework. The real direct dependencies are the Charm TUI stack
(`charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`) and
`filippo.io/age` for encryption; see `go.mod` for the full list.

### Add Dependency

```bash
# Add a new dependency (example)
go get filippo.io/age@latest

# Add a specific version
go get filippo.io/age@v1.3.1

# Tidy up (remove unused, add missing)
go mod tidy
```

### Update Dependencies

```bash
# Update all dependencies
go get -u ./...

# Update specific dependency
go get -u filippo.io/age

# Tidy after updates
go mod tidy
```

### List Dependencies

```bash
# List all dependencies
go list -m all

# List direct dependencies only
go list -m -f '{{if not .Indirect}}{{.}}{{end}}' all

# Check for available updates
go list -u -m all
```

### Vendor Dependencies

```bash
# Create vendor directory (optional)
go mod vendor

# Build with vendor
go build -mod=vendor -o build/proxsave ./cmd/proxsave
```

---

## Code Guidelines

### Go Best Practices

- **Follow [Effective Go](https://golang.org/doc/effective_go.html)**
- **Use `gofmt`** for formatting (automatic with `go fmt` or `make fmt`)
- **Run `make lint`** before committing (see the structural guards below)
- **Write godoc comments** for exported functions
- **Handle errors explicitly** (no silent failures)
- **Use `context.Context`** for cancellation

### Lint and structural guards

`make lint` runs `go vet ./...`, `golint` if installed, and two repo-specific
guards that fail the build on an architectural regression:

- `check-no-tview`: fails if any `cmd`, `internal`, or `pkg` code imports the legacy
  `rivo/tview` or `gdamore/tcell` stack. The Charm rebuild removed them and they must
  not come back.
- `check-serverbot-leaf`: fails if `internal/serverbot` imports
  `internal/health`, `internal/notify`, `internal/orchestrator`, `internal/config`, or
  `internal/identity`. The transport must stay a leaf (logging, version, and the
  standard library only); see [NOTIFICATIONS.md](NOTIFICATIONS.md).

### Code Style

**Naming conventions**:
```go
// Exported (public)
func BackupOrchestrator() {}
type Config struct {}
const MaxRetries = 3

// Unexported (private)
func backupHelper() {}
type internalState struct {}
const defaultTimeout = 30
```

**Error handling**:
```go
// Good: Explicit error handling
result, err := doSomething()
if err != nil {
    return fmt.Errorf("failed to do something: %w", err)
}

// Bad: Ignoring errors
result, _ := doSomething()
```

**Context usage**:
```go
// Good: Pass context
func ProcessBackup(ctx context.Context, cfg *Config) error {
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
        // Process backup
    }
}
```

### Testing Guidelines

**Test file naming**: `*_test.go`

**Test function naming**: `TestFunctionName`

**Table-driven tests**:
```go
func TestCompressionAlgorithms(t *testing.T) {
    tests := []struct {
        name       string
        algorithm  string
        level      int
        wantErr    bool
    }{
        {"XZ Level 6", "xz", 6, false},
        {"Zstd Level 3", "zstd", 3, false},
        {"Invalid Algorithm", "invalid", 0, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test logic
        })
    }
}
```

### Documentation

**Godoc comments**:
```go
// BackupOrchestrator coordinates the entire backup process.
// It handles collection, compression, encryption, and storage distribution.
//
// Parameters:
//   - ctx: Context for cancellation
//   - cfg: Configuration settings
//
// Returns:
//   - error: nil on success, error details on failure
func BackupOrchestrator(ctx context.Context, cfg *Config) error {
    // Implementation
}
```

**Add tests for new features**:
- Unit tests for all new functions
- Integration tests for workflows
- Benchmark tests for performance-critical code

**Update documentation for changes**:
- Update relevant `docs/*.md` files
- Update README.md if user-facing changes
- Add examples for new features

---

## Contributing

We welcome contributions! Here's how you can help:

### Ways to Contribute

- **Report bugs**: open an issue with detailed reproduction steps
- **Suggest features**: share your ideas for improvements
- **Improve documentation**: fix typos, add examples, clarify instructions
- **Submit code**: fork, create a branch, and submit a pull request

### Contribution Workflow

**1. Fork and clone**:
```bash
# Fork on GitHub, then:
git clone https://github.com/YOUR_USERNAME/proxsave.git
cd proxsave
git remote add upstream https://github.com/tis24dev/proxsave.git
```

**2. Create feature branch**:
```bash
git checkout -b feature/your-feature-name
```

**3. Make changes**:
```bash
# Write code
# Add tests
# Update documentation

# Format code
go fmt ./...

# Run tests
go test ./...

# Run vet and the structural guards
make lint
```

**4. Commit changes**:
```bash
# Atomic commits with clear messages
git add .
git commit -m "Add: feature description

Detailed explanation of changes:
- Added X functionality
- Updated Y module
- Fixed Z issue"
```

**Commit message format**:
- **Add**: New feature or functionality
- **Update**: Improvement to existing feature
- **Fix**: Bug fix
- **Refactor**: Code refactoring without functional changes
- **Docs**: Documentation changes
- **Test**: Test additions or updates

**5. Push and create PR**:
```bash
git push origin feature/your-feature-name
# Create pull request on GitHub
```

**6. Code review**:
- Address review comments
- Push updates to the same branch
- PR automatically updates

### Pull Request Guidelines

**PR description should include**:
- What problem does it solve?
- How does it solve it?
- Any breaking changes?
- Testing performed

**Example PR description**:
```markdown
## Description
Adds support for GFS retention policies with configurable tiers.

## Problem
Users need long-term retention policies that comply with Grandfather-Father-Son backup strategies.

## Solution
- Implemented GFS retention logic in `internal/retention/gfs.go`
- Added configuration variables: RETENTION_DAILY, RETENTION_WEEKLY, RETENTION_MONTHLY, RETENTION_YEARLY
- Updated orchestrator to call GFS retention when RETENTION_POLICY=gfs

## Breaking Changes
None. Existing simple retention remains default.

## Testing
- Unit tests for GFS logic
- Integration test with 365 daily backups
- Manual testing with real PVE installation
```

---

## Useful Commands

### Build & Development

```bash
# Development build
make build

# Optimized build
go build -ldflags="-s -w" -o build/proxsave ./cmd/proxsave

# Run without building
make run

# Clean build artifacts
make clean
```

### Testing & Quality

```bash
# All tests
go test ./...

# With coverage
go test -cover ./...
make test-coverage

# Specific package
go test ./internal/config

# Verbose
go test -v ./...

# Benchmarks
go test -bench=. ./...

# Lint (go vet + structural guards: check-no-tview, check-serverbot-leaf)
make lint
```

### Dependencies

```bash
# Add dependency
go get filippo.io/age@latest

# Update all dependencies
go get -u ./...

# Tidy up
go mod tidy

# List dependencies
go list -m all

# Vendor dependencies
go mod vendor
```

### Debugging

```bash
# Run with delve debugger
dlv debug ./cmd/proxsave

# Run with race detector
go run -race ./cmd/proxsave

# Build with debug symbols
go build -gcflags="all=-N -l" -o build/proxsave-debug ./cmd/proxsave
```

### rclone Utilities (for cloud development)

```bash
# List remotes
rclone listremotes

# Show remote config
rclone config show gdrive

# List files (long format)
rclone lsl gdrive:pbs-backups/

# List files (short format)
rclone lsf gdrive:pbs-backups/

# Check quota
rclone about gdrive:

# Copy local → remote
rclone copy /local/file.txt gdrive:pbs-backups/

# Copy remote → local
rclone copy gdrive:pbs-backups/file.txt /local/

# Sync (WARNING: deletes non-matching files)
rclone sync /local/dir/ gdrive:pbs-backups/

# Create directory
rclone mkdir gdrive:pbs-backups/subdir

# Delete file
rclone deletefile gdrive:pbs-backups/file.txt

# Delete directory (recursive)
rclone purge gdrive:pbs-backups/old/

# Verify integrity
rclone check /local/dir/ gdrive:pbs-backups/ --checksum
```

---

## Related Documentation

### User Documentation
- **[Docs Index](README.md)** - Documentation hub for the `docs/` tree
- **[Configuration Guide](CONFIGURATION.md)** - All configuration variables
- **[CLI Reference](CLI_REFERENCE.md)** - Command-line flags

### Contributor Documentation
- **[Collector Architecture](COLLECTOR_ARCHITECTURE.md)** - Collector recipes, bricks, and `dual`
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Restore internals and compatibility flow
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common issues

### External Resources
- **[Effective Go](https://golang.org/doc/effective_go.html)** - Go best practices
- **[Go Modules](https://golang.org/ref/mod)** - Dependency management
- **[rclone Documentation](https://rclone.org/docs/)** - Cloud storage integration
- **[AGE Specification](https://age-encryption.org/v1)** - Encryption format

---

## Development Checklist

Use this checklist when contributing:

```
Before Coding:
□ Fork and clone repository
□ Create feature branch
□ Review related issues/PRs
□ Plan implementation approach

During Development:
□ Follow Go best practices
□ Write clear, self-documenting code
□ Add godoc comments for exports
□ Handle errors explicitly
□ Use context for cancellation

Testing:
□ Write unit tests for new functions
□ Add integration tests for workflows
□ Run all tests (go test ./...)
□ Check coverage (make coverage-check)
□ Run vet and guards (make lint)

Documentation:
□ Update relevant docs/*.md files
□ Add usage examples
□ Update docs/README.md and architecture docs if navigation changes
□ Write clear commit messages

Before Submitting PR:
□ Rebase on latest main
□ Run full test suite
□ Verify no breaking changes
□ Check documentation is updated
□ Write clear PR description

After PR Submission:
□ Address review comments
□ Push updates to same branch
```

---

## Code Review Guidelines

**For reviewers**:
- Verify tests pass
- Check code follows Go best practices
- Ensure documentation is updated
- Look for potential security issues
- Verify error handling is robust
- Check for race conditions (use `-race`)
- Ensure commit messages are clear

**For contributors**:
- Respond to all comments
- Push updates to the same branch (no force push after review starts)
- Mark resolved comments

---

## License

This project is licensed under the **MIT License** - see the [LICENSE](../LICENSE) file for details.

---

## Contact & Support

- **GitHub Issues**: https://github.com/tis24dev/proxsave/issues
- **Pull Requests**: https://github.com/tis24dev/proxsave/pulls
- **Discussions**: Use GitHub Discussions for questions and ideas

**For security vulnerabilities**: please email privately instead of opening a public issue.
