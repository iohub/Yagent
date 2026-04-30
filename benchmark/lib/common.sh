#!/bin/bash
# Common utilities for CodeActor benchmark

set -euo pipefail

BENCHMARK_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$BENCHMARK_ROOT/results"
SERVER_HOST="${CODECACTOR_HOST:-localhost}"
SERVER_PORT="${CODECACTOR_PORT:-9080}"
CODECACTOR_URL="http://${SERVER_HOST}:${SERVER_PORT}"
CLI_DIR="$BENCHMARK_ROOT/../clients/nodejs-cli"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${BLUE}[INFO]${NC}  $(date +%H:%M:%S) $*"; }
log_pass()  { echo -e "${GREEN}[PASS]${NC}  $(date +%H:%M:%S) $*"; }
log_fail()  { echo -e "${RED}[FAIL]${NC}  $(date +%H:%M:%S) $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $(date +%H:%M:%S) $*"; }

# Wait for the server to be ready
wait_for_server() {
    local max_wait=30
    local waited=0
    log_info "Waiting for CodeActor server at $CODECACTOR_URL ..."
    while ! curl -s "$CODECACTOR_URL/api/history" > /dev/null 2>&1; do
        sleep 1
        waited=$((waited + 1))
        if [ $waited -ge $max_wait ]; then
            log_fail "Server not reachable after ${max_wait}s"
            return 1
        fi
    done
    log_info "Server is ready"
}

# Submit a task via HTTP API and wait for completion
# Usage: run_task <project_dir> <task_description> <timeout_seconds>
# Returns: task_id on stdout, task result in TASK_RESULT global
run_task() {
    local project_dir="$1"
    local task_desc="$2"
    local timeout="${3:-600}"

    # Escape task_desc for JSON
    local json_task
    json_task=$(python3 -c "import json,sys; print(json.dumps(sys.argv[1]))" "$task_desc")

    log_info "Submitting task to $project_dir ..."
    local response
    response=$(curl -s -X POST "$CODECACTOR_URL/api/start_task" \
        -H "Content-Type: application/json" \
        -d "{\"project_dir\": \"$project_dir\", \"task_desc\": $json_task}")

    local task_id
    task_id=$(echo "$response" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('task_id',''))")
    if [ -z "$task_id" ]; then
        log_fail "Failed to create task: $response"
        return 1
    fi
    log_info "Task created: $task_id"

    # Poll for completion
    local elapsed=0
    local interval=5
    while [ $elapsed -lt $timeout ]; do
        sleep "$interval"
        elapsed=$((elapsed + interval))

        local status_json
        status_json=$(curl -s "$CODECACTOR_URL/api/task_status?task_id=$task_id")
        local status
        status=$(echo "$status_json" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status',''))")

        if [ "$status" = "finished" ]; then
            TASK_RESULT=$(echo "$status_json" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result',''))")
            TASK_ID="$task_id"
            log_info "Task $task_id finished in ${elapsed}s"
            return 0
        elif [ "$status" = "failed" ]; then
            TASK_ERROR=$(echo "$status_json" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('error',''))")
            TASK_ID="$task_id"
            log_fail "Task $task_id failed: $TASK_ERROR"
            return 1
        fi
    done

    log_fail "Task $task_id timed out after ${timeout}s"
    return 124
}

# Read task description from file (strip comments)
read_task_md() {
    local md_file="$1"
    cat "$md_file"
}

# Record benchmark result
record_result() {
    local task_name="$1"
    local category="$2"
    local passed="$3"
    local duration="$4"
    local task_id="${5:-}"
    local notes="${6:-}"

    mkdir -p "$RESULTS_DIR"
    local result_file="$RESULTS_DIR/results.jsonl"
    python3 -c "
import json
json.dump({
    'task': '$task_name',
    'category': '$category',
    'passed': '$passed',
    'duration_sec': '$duration',
    'task_id': '$task_id',
    'notes': '$notes',
    'timestamp': '$(date -Iseconds)'
}, sys.stdout)
print()
" >> "$result_file"
}

# Print final summary
print_summary() {
    echo ""
    echo "============================================="
    echo "         BENCHMARK RESULTS SUMMARY"
    echo "============================================="
    local total=0 passed=0 failed=0
    if [ -f "$RESULTS_DIR/results.jsonl" ]; then
        while IFS= read -r line; do
            total=$((total + 1))
            local p
            p=$(echo "$line" | python3 -c "import json,sys; print(json.load(sys.stdin)['passed'])")
            if [ "$p" = "true" ]; then
                passed=$((passed + 1))
            else
                failed=$((failed + 1))
                local name
                name=$(echo "$line" | python3 -c "import json,sys; print(json.load(sys.stdin)['task'])")
                log_fail "  Failed: $name"
            fi
        done < "$RESULTS_DIR/results.jsonl"
    fi
    echo ""
    echo "Total: $total | Passed: $passed | Failed: $failed"
    if [ $total -gt 0 ]; then
        local rate=$((passed * 100 / total))
        echo "Pass Rate: ${rate}%"
    fi
    echo "============================================="
}
