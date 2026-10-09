.PHONY: build run test coverage clean generate build-benchmark test-benchmark clean-benchmark

CLI_APP_NAME=syntrix-cli
APP_NAME=syntrix
BENCHMARK_APP_NAME=syntrix-benchmark
BUILD_DIR=bin

ifeq ($(OS),Windows_NT)
	MKDIR_P = powershell -NoProfile -Command "if (-not (Test-Path '$(BUILD_DIR)')) { New-Item -ItemType Directory -Path '$(BUILD_DIR)' | Out-Null }"
	RM_RF = powershell -NoProfile -Command "if (Test-Path '$(BUILD_DIR)') { Remove-Item -Recurse -Force '$(BUILD_DIR)' }"
	EXE_EXT = .exe
else
    MKDIR_P = mkdir -p $(BUILD_DIR)
    RM_RF = rm -rf $(BUILD_DIR)
	EXE_EXT =
endif

APP_BIN=$(BUILD_DIR)/$(APP_NAME)$(EXE_EXT)
CLI_BIN=$(BUILD_DIR)/$(CLI_APP_NAME)$(EXE_EXT)
BENCHMARK_BIN=$(BUILD_DIR)/$(BENCHMARK_APP_NAME)$(EXE_EXT)

build:
	@$(MKDIR_P)
	@echo "Building $(CLI_APP_NAME)..."
	@go build -o $(CLI_BIN) ./cmd/syntrix-cli
	@echo "Building $(APP_NAME)..."
	@go build -o $(APP_BIN) ./cmd/syntrix
	@echo "Building $(BENCHMARK_APP_NAME)..."
	@go build -o $(BENCHMARK_BIN) ./cmd/syntrix-benchmark

run: build
	@echo "Running $(APP_NAME)..."
	@$(APP_BIN) --all

test:
	@echo Running tests...
	@go test ./... -count=1 -timeout=5m

ifeq ($(OS),Windows_NT)
    COVERAGE_CMD = scripts\coverage.cmd
else
    COVERAGE_CMD = ./scripts/coverage.sh
endif

coverage:
	@echo Running tests with coverage...
	@$(COVERAGE_CMD)

clean:
	@echo "Cleaning..."
	@$(RM_RF)

generate:
	@echo "Running go generate..."
	@go generate ./...

# Benchmark tool targets
build-benchmark:
	@$(MKDIR_P)
	@echo "Building $(BENCHMARK_APP_NAME)..."
	@go build -o $(BENCHMARK_BIN) ./cmd/syntrix-benchmark

test-benchmark:
	@echo "Running benchmark tool tests..."
	@go test ./pkg/benchmark/... -count=1 -timeout=30s

clean-benchmark:
	@echo "Cleaning benchmark binary..."
	@rm -f $(BENCHMARK_BIN)
