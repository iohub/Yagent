#!/usr/bin/env python3
"""Serial benchmark runner for CodeActor Agent.
Runs tasks one at a time, waits for completion, validates, records results.
"""
import json, os, subprocess, sys, time, urllib.request, urllib.error
from datetime import datetime

SERVER = "http://localhost:9080"
TIMEOUT = 900  # 15 min per task
RESULTS_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "results")

TASKS = [
    # Rust (medium) - 600s timeout
    ("rust_01_lru_cache", "Thread-Safe LRU Cache", 600),
    ("rust_02_json_parser", "JSON Parser Subset", 600),
    ("rust_03_matrix_multiply", "Parallel Matrix Multiplication", 600),
    ("rust_04_template_engine", "Simple Template Engine", 600),
    ("rust_05_command_pipeline", "Shell Command Pipeline", 600),
    # Python (high) - 900s timeout
    ("python_01_di_framework", "Decorator DI Framework", 900),
    ("python_02_async_eventloop", "Mini Async Event Loop", 900),
    ("python_03_regex_engine", "NFA Regex Engine", 900),
    ("python_04_mini_redis", "Redis-Compatible Server", 900),
    ("python_05_distributed_lock", "Distributed Lock Manager", 900),
    # Non-code (high) - 900s timeout
    ("noncode_01_arch_docs", "Architecture Documentation", 900),
    ("noncode_02_security_audit", "Security Vulnerability Audit", 900),
    ("noncode_03_tech_translate", "Tech Spec Translation", 600),
    ("noncode_04_db_migration", "DB Migration Plan", 900),
    ("noncode_05_perf_analysis", "Performance Bottleneck Analysis", 900),
]

def api_call(method, path, data=None):
    url = f"{SERVER}{path}"
    if data:
        req = urllib.request.Request(url, data=json.dumps(data).encode(), headers={"Content-Type": "application/json"}, method=method)
    else:
        req = urllib.request.Request(url, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        return {"error": str(e), "body": e.read().decode()}
    except Exception as e:
        return {"error": str(e)}

def submit_task(project_dir, task_desc):
    resp = api_call("POST", "/api/start_task", {"project_dir": project_dir, "task_desc": task_desc})
    return resp.get("task_id", "")

def get_task_status(task_id):
    resp = api_call("GET", f"/api/task_status?task_id={task_id}")
    return resp.get("status", "unknown"), resp.get("result", ""), resp.get("error", "")

def wait_for_task(task_id, timeout):
    interval = 5
    elapsed = 0
    while elapsed < timeout:
        time.sleep(interval)
        elapsed += interval
        status, result, error = get_task_status(task_id)
        mins = elapsed // 60
        secs = elapsed % 60
        print(f"  [{mins:02d}:{secs:02d}] status={status}", flush=True)
        if status == "finished":
            return True, result, elapsed
        elif status == "failed":
            return False, error, elapsed
    return False, f"Timeout after {timeout}s", elapsed

def run_setup(task_name):
    task_dir = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tasks", task_name)
    setup_script = os.path.join(task_dir, "setup.sh")
    if os.path.exists(setup_script):
        subprocess.run(["bash", setup_script], check=True, capture_output=True)

def run_validate(task_name, repo_dir):
    task_dir = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tasks", task_name)
    validate_script = os.path.join(task_dir, "validate.sh")
    if os.path.exists(validate_script):
        result = subprocess.run(["bash", validate_script, repo_dir], capture_output=True, text=True, timeout=120)
        return result.returncode == 0, result.stdout + result.stderr
    return True, "No validate.sh"

def main():
    os.makedirs(RESULTS_DIR, exist_ok=True)
    results_file = os.path.join(RESULTS_DIR, "results.jsonl")
    # Clear previous results
    with open(results_file, 'w') as f:
        f.write("")

    total = len(TASKS)
    passed = 0
    failed = 0

    print(f"{'='*60}")
    print(f"CodeActor Benchmark — {total} tasks")
    print(f"Server: {SERVER}")
    print(f"Started: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"{'='*60}")
    print()

    for idx, (task_name, task_label, task_timeout) in enumerate(TASKS):
        task_dir = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tasks", task_name)
        repo_dir = f"/tmp/repos/{task_name}"
        task_md = os.path.join(task_dir, "task.md")

        print(f"{'─'*60}")
        print(f"[{idx+1}/{total}] {task_label} ({task_name})")
        print(f"{'─'*60}")

        # Setup
        print(f"  Setup...", end=" ", flush=True)
        try:
            run_setup(task_name)
            print("OK")
        except Exception as e:
            print(f"FAILED: {e}")
            failed += 1
            record(results_file, task_name, "mixed", False, 0, "", f"setup_failed: {e}")
            continue

        # Read task description
        with open(task_md) as f:
            task_desc = f.read()

        # Submit
        print(f"  Submit...", end=" ", flush=True)
        task_id = submit_task(repo_dir, task_desc)
        if not task_id:
            print("FAILED (no task_id)")
            failed += 1
            record(results_file, task_name, "mixed", False, 0, "", "submit_failed")
            continue
        print(f"task_id={task_id[:8]}...")

        # Wait
        start_time = time.time()
        success, output, elapsed = wait_for_task(task_id, task_timeout)
        duration = int(time.time() - start_time)

        if not success:
            print(f"  TASK FAILED/TIMEOUT ({duration}s): {output[:200]}")
            failed += 1
            record(results_file, task_name, "mixed", False, duration, task_id, f"task_failed: {output[:200]}")
            continue

        # Validate
        print(f"  Validate...", end=" ", flush=True)
        try:
            v_ok, v_out = run_validate(task_name, repo_dir)
            if v_ok:
                print(f"PASS ({duration}s)")
                passed += 1
                record(results_file, task_name, "mixed", True, duration, task_id, "")
            else:
                print(f"FAILED ({duration}s)")
                print(f"  {v_out[-500:]}")
                failed += 1
                record(results_file, task_name, "mixed", False, duration, task_id, f"validation_failed")
        except Exception as e:
            print(f"ERROR: {e}")
            failed += 1
            record(results_file, task_name, "mixed", False, duration, task_id, f"validation_error: {e}")

        print()

    # Summary
    print(f"{'='*60}")
    print(f"BENCHMARK COMPLETE")
    print(f"Total: {total} | Passed: {passed} | Failed: {failed}")
    if total > 0:
        print(f"Pass Rate: {passed*100//total}%")
    print(f"Finished: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"{'='*60}")

def record(results_file, task_name, category, passed, duration, task_id, notes):
    entry = {
        "task": task_name,
        "category": category,
        "passed": passed,
        "duration_sec": duration,
        "task_id": task_id,
        "notes": notes,
        "timestamp": datetime.now().isoformat()
    }
    with open(results_file, 'a') as f:
        f.write(json.dumps(entry, ensure_ascii=False) + "\n")

if __name__ == "__main__":
    main()
