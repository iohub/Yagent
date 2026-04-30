#!/bin/bash
set -e
REPO="/tmp/repos/python_05_distributed_lock"
rm -rf "$REPO"
mkdir -p "$REPO/dist_lock"
cd "$REPO"
touch dist_lock/__init__.py

cat > dist_lock/manager.py << 'PYEOF'
# TODO: Implement a file-based distributed lock manager

class LockManager:
    """Distributed lock manager using file-based locking."""
    def __init__(self, lock_dir='/tmp/dist_locks'):
        pass
    def acquire(self, lock_name: str, ttl_seconds: float, owner_id: str = None) -> bool:
        pass
    def release(self, lock_name: str, owner_id: str) -> bool:
        pass
    def renew(self, lock_name: str, ttl_seconds: float, owner_id: str) -> bool:
        pass
    def get_owner(self, lock_name: str) -> str:
        pass
    def get_ttl(self, lock_name: str) -> float:
        pass
    def wait_for_lock(self, lock_name: str, timeout: float, ttl_seconds: float) -> bool:
        pass

class Lock:
    """Context manager for distributed locks."""
    def __init__(self, manager, lock_name, ttl_seconds, owner_id=None):
        pass
    def __enter__(self):
        pass
    def __exit__(self, *args):
        pass
PYEOF

mkdir -p tests
cat > tests/test_lock.py << 'PYEOF'
import sys, os, time, threading, shutil
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from dist_lock.manager import LockManager, Lock

LOCK_DIR = "/tmp/dist_lock_test"

def setup_module():
    if os.path.exists(LOCK_DIR):
        shutil.rmtree(LOCK_DIR)
    os.makedirs(LOCK_DIR, exist_ok=True)

def teardown_module():
    shutil.rmtree(LOCK_DIR, ignore_errors=True)

def test_acquire_basic():
    mgr = LockManager(lock_dir=LOCK_DIR)
    assert mgr.acquire("resource1", ttl_seconds=10, owner_id="proc1")
    owner = mgr.get_owner("resource1")
    assert owner == "proc1"

def test_cannot_acquire_held_lock():
    mgr = LockManager(lock_dir=LOCK_DIR)
    assert mgr.acquire("resource2", ttl_seconds=10, owner_id="proc1")
    assert not mgr.acquire("resource2", ttl_seconds=10, owner_id="proc2")

def test_release():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource3", ttl_seconds=10, owner_id="proc1")
    assert mgr.release("resource3", owner_id="proc1")
    # Now proc2 should be able to acquire
    assert mgr.acquire("resource3", ttl_seconds=10, owner_id="proc2")

def test_release_wrong_owner_fails():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource4", ttl_seconds=10, owner_id="proc1")
    assert not mgr.release("resource4", owner_id="proc2")

def test_ttl_expiry():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource5", ttl_seconds=0.3, owner_id="proc1")
    time.sleep(0.5)
    # After TTL, lock should be expired and acquirable by proc2
    assert mgr.acquire("resource5", ttl_seconds=10, owner_id="proc2")

def test_renew():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource6", ttl_seconds=0.5, owner_id="proc1")
    time.sleep(0.2)
    assert mgr.renew("resource6", ttl_seconds=1.0, owner_id="proc1")
    time.sleep(0.7)  # Would have expired without renewal
    assert not mgr.acquire("resource6", ttl_seconds=10, owner_id="proc2")

def test_wait_for_lock_timeout():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource7", ttl_seconds=5, owner_id="proc1")

    def try_acquire():
        return mgr.wait_for_lock("resource7", timeout=0.3, ttl_seconds=1)

    result = try_acquire()
    assert result is False  # Should timeout because proc1 holds the lock

def test_wait_for_lock_success():
    mgr = LockManager(lock_dir=LOCK_DIR)
    mgr.acquire("resource8", ttl_seconds=0.2, owner_id="proc1")

    acquired = [False]
    def wait_and_acquire():
        acquired[0] = mgr.wait_for_lock("resource8", timeout=2.0, ttl_seconds=5)

    t = threading.Thread(target=wait_and_acquire)
    t.start()
    time.sleep(0.4)  # Wait for lock to expire
    t.join(timeout=3)
    assert acquired[0], "Should have acquired lock after expiry"

def test_context_manager():
    mgr = LockManager(lock_dir=LOCK_DIR)
    with Lock(mgr, "resource9", ttl_seconds=10):
        assert not mgr.acquire("resource9", ttl_seconds=1, owner_id="other")
    # After context exit, lock should be released
    assert mgr.acquire("resource9", ttl_seconds=1, owner_id="other")

def test_concurrent_unique_owners():
    mgr = LockManager(lock_dir=LOCK_DIR)
    results = []
    lock_obj = threading.Lock()

    def worker(name):
        for _ in range(20):
            if mgr.acquire("resource10", ttl_seconds=0.1, owner_id=name):
                with lock_obj:
                    results.append(name)
                time.sleep(0.02)
                mgr.release("resource10", owner_id=name)
            time.sleep(0.01)

    threads = [threading.Thread(target=worker, args=(f"w{i}",)) for i in range(3)]
    for t in threads: t.start()
    for t in threads: t.join()
    assert len(results) > 0

PYEOF

echo "python_05_distributed_lock setup done"
