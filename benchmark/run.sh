#!/bin/bash
# CodeActor Agent Benchmark Runner
# Usage: ./run.sh [--category rust|python|noncode|all] [--timeout seconds] [--task TASK_NAME]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

CATEGORY="all"
TIMEOUT=900
SINGLE_TASK=""
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Parse args
while [ $# -gt 0 ]; do
    case "$1" in
        --category) CATEGORY="$2"; shift 2 ;;
        --timeout) TIMEOUT="$2"; shift 2 ;;
        --task) SINGLE_TASK="$2"; shift 2 ;;
        --no-server) NO_SERVER=true; shift ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# Ensure pytest is available
if ! python3 -m pytest --version > /dev/null 2>&1; then
    log_info "Installing pytest..."
    pip3 install --break-system-packages pytest -q 2>&1 || true
fi

# Start server if needed
if [ "${NO_SERVER:-false}" = false ]; then
    log_info "Building server..."
    cd "$SCRIPT_DIR/.."
    go build -o codeactor . || { log_fail "Build failed"; exit 1; }

    log_info "Starting server..."
    ./codeactor http --disable-agents=repo,coding,chat > /tmp/benchmark-server.log 2>&1 &
    SERVER_PID=$!
    sleep 2
    wait_for_server || exit 1
else
    wait_for_server || exit 1
fi

# Define tasks
TASKS=()

# Rust tasks (medium difficulty)
RUST_TASKS=(
    "rust_01_lru_cache:Thread-Safe LRU Cache:600"
    "rust_02_json_parser:JSON Parser Subset:600"
    "rust_03_matrix_multiply:Parallel Matrix Multiplication:600"
    "rust_04_template_engine:Simple Template Engine:600"
    "rust_05_command_pipeline:Shell Command Pipeline:600"
)

# Python tasks (high difficulty)
PYTHON_TASKS=(
    "python_01_di_framework:Decorator DI Framework:900"
    "python_02_async_eventloop:Mini Async Event Loop:900"
    "python_03_regex_engine:NFA Regex Engine:900"
    "python_04_mini_redis:Redis-Compatible Server:900"
    "python_05_distributed_lock:Distributed Lock Manager:900"
)

# Non-code tasks (high difficulty)
NONCODE_TASKS=(
    "noncode_01_arch_docs:Architecture Documentation:900"
    "noncode_02_security_audit:Security Vulnerability Audit:900"
    "noncode_03_tech_translate:Tech Spec Translation:600"
    "noncode_04_db_migration:DB Migration Plan:900"
    "noncode_05_perf_analysis:Performance Bottleneck Analysis:900"
)

# Build task list based on category
build_task_list() {
    case "$CATEGORY" in
        rust)    for t in "${RUST_TASKS[@]}"; do TASKS+=("$t"); done ;;
        python)  for t in "${PYTHON_TASKS[@]}"; do TASKS+=("$t"); done ;;
        noncode) for t in "${NONCODE_TASKS[@]}"; do TASKS+=("$t"); done ;;
        all)
            for t in "${RUST_TASKS[@]}"; do TASKS+=("$t"); done
            for t in "${PYTHON_TASKS[@]}"; do TASKS+=("$t"); done
            for t in "${NONCODE_TASKS[@]}"; do TASKS+=("$t"); done
            ;;
    esac
}

build_task_list

# Reset results
rm -f "$RESULTS_DIR/results.jsonl"
mkdir -p "$RESULTS_DIR"

# Run benchmark
TOTAL=${#TASKS[@]}
CURRENT=0

for task_spec in "${TASKS[@]}"; do
    CURRENT=$((CURRENT + 1))
    IFS=':' read -r task_name task_label task_timeout <<< "$task_spec"

    # Skip if single task specified and doesn't match
    if [ -n "$SINGLE_TASK" ] && [ "$task_name" != "$SINGLE_TASK" ]; then
        continue
    fi

    TASK_DIR="$SCRIPT_DIR/tasks/$task_name"
    if [ ! -d "$TASK_DIR" ]; then
        log_warn "Task directory not found: $TASK_DIR, skipping"
        continue
    fi

    echo ""
    echo "============================================="
    log_info "[$CURRENT/$TOTAL] Running: $task_label ($task_name)"
    echo "============================================="

    REPO_DIR="/tmp/repos/$task_name"

    # Setup
    log_info "Setting up task repo..."
    if [ -f "$TASK_DIR/setup.sh" ]; then
        bash "$TASK_DIR/setup.sh" || {
            log_fail "Setup failed for $task_name"
            record_result "$task_name" "$CATEGORY" false 0 "" "setup_failed"
            continue
        }
    fi

    # Read task description
    TASK_DESC=$(cat "$TASK_DIR/task.md")

    # Run
    START_TIME=$(date +%s)
    if run_task "$REPO_DIR" "$TASK_DESC" "$task_timeout"; then
        END_TIME=$(date +%s)
        DURATION=$((END_TIME - START_TIME))

        # Validate
        log_info "Running validation..."
        if [ -f "$TASK_DIR/validate.sh" ]; then
            if bash "$TASK_DIR/validate.sh" "$REPO_DIR" "$TASK_ID" 2>&1; then
                log_pass "Task $task_name PASSED (${DURATION}s)"
                record_result "$task_name" "$CATEGORY" true "$DURATION" "$TASK_ID" ""
            else
                log_fail "Task $task_name FAILED validation (${DURATION}s)"
                record_result "$task_name" "$CATEGORY" false "$DURATION" "$TASK_ID" "validation_failed"
            fi
        else
            log_warn "No validate.sh found, skipping validation"
            record_result "$task_name" "$CATEGORY" true "$DURATION" "$TASK_ID" "no_validator"
        fi
    else
        END_TIME=$(date +%s)
        DURATION=$((END_TIME - START_TIME))
        log_fail "Task $task_name FAILED (${DURATION}s)"
        record_result "$task_name" "$CATEGORY" false "$DURATION" "" "task_failed_or_timeout"
    fi
done

print_summary
