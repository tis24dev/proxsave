.PHONY: build test clean run build-release test-coverage lint fmt deps help coverage coverage-check

COVERAGE_THRESHOLD ?= 50.0

# Build del progetto
build:
	@echo "Building proxsave..."
	@BUILD_TIME=$$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
	go build -ldflags="-X 'main.buildTime=$$BUILD_TIME'" -o build/proxsave ./cmd/proxsave

# Build ottimizzato per release
build-release:
	@echo "Building release..."
	@BUILD_TIME=$$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
	go build -ldflags="-s -w -X 'main.buildTime=$$BUILD_TIME'" -o build/proxsave ./cmd/proxsave

# Test
test:
	go test -v ./...

# Test con coverage
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Full coverage report (all packages)
coverage:
	@echo "Running coverage across all packages..."
	@go test -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -n 1

# Enforce minimum coverage threshold
coverage-check:
	@echo "Running coverage check (threshold $(COVERAGE_THRESHOLD)% )..."
	@go test -coverpkg=./... -coverprofile=coverage.out ./...
	@total=$$(go tool cover -func=coverage.out | grep total: | awk '{print $$3}' | sed 's/%//'); \
		echo "Total coverage: $$total%"; \
		if awk -v total="$$total" -v threshold="$(COVERAGE_THRESHOLD)" 'BEGIN { exit !(total+0 >= threshold+0) }'; then \
			echo "Coverage check passed."; \
		else \
			echo "Coverage threshold not met (need >= $(COVERAGE_THRESHOLD)% )."; \
			exit 1; \
		fi

# Lint
lint:
	go vet ./...
	@command -v golint >/dev/null 2>&1 && golint ./... || echo "golint not installed"

# Format code
fmt:
	go fmt ./...

# Clean build artifacts
clean:
	rm -rf build/
	rm -f coverage.out

# Run in development
run:
	go run ./cmd/proxsave

# Install/update dependencies
deps:
	go mod download
	go mod tidy

# Help
help:
	@echo "Available targets:"
	@echo "  build         - Build the project"
	@echo "  build-release - Build optimized release binary"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  coverage      - Generate coverage profile across all packages"
	@echo "  coverage-check - Run coverage and enforce minimum threshold"
	@echo "  lint          - Run linters"
	@echo "  fmt           - Format Go code"
	@echo "  clean         - Remove build artifacts"
	@echo "  run           - Run in development mode"
	@echo "  deps          - Download and tidy dependencies"
