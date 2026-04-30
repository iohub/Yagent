#!/bin/bash
set -e
REPO="/tmp/repos/python_04_mini_redis"
rm -rf "$REPO"
mkdir -p "$REPO/mini_redis"
cd "$REPO"
touch mini_redis/__init__.py

cat > mini_redis/server.py << 'PYEOF'
# TODO: Implement a Redis-compatible TCP server

class RedisServer:
    """TCP server supporting Redis protocol commands."""
    def __init__(self, host='127.0.0.1', port=6379, data_dir='/tmp/mini_redis_data'):
        pass
    def start(self):
        pass
    def stop(self):
        pass
PYEOF

cat > mini_redis/store.py << 'PYEOF'
# TODO: Implement key-value store with expiry and persistence

class DataStore:
    """In-memory key-value store with TTL and AOF persistence."""
    pass
PYEOF

mkdir -p tests
cat > tests/test_server.py << 'PYEOF'
import sys, os, time, socket, threading, json, shutil
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

DATA_DIR = "/tmp/mini_redis_test_data"
if os.path.exists(DATA_DIR):
    shutil.rmtree(DATA_DIR)
os.makedirs(DATA_DIR, exist_ok=True)

from mini_redis.server import RedisServer

def send_command(sock, *args):
    """Send Redis protocol command."""
    parts = [f"*{len(args)}\r\n"]
    for a in args:
        parts.append(f"${len(str(a))}\r\n{str(a)}\r\n")
    msg = "".join(parts).encode()
    sock.sendall(msg)
    # Read response
    resp = b""
    while True:
        try:
            sock.settimeout(0.1)
            chunk = sock.recv(4096)
            if not chunk:
                break
            resp += chunk
        except socket.timeout:
            break
    sock.settimeout(None)
    return resp.decode()

@pytest.fixture(scope="module")
def server():
    s = RedisServer(host='127.0.0.1', port=16379, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    yield s
    s.stop()
    time.sleep(0.1)

def test_set_and_get():
    s = RedisServer(host='127.0.0.1', port=16380, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16380))
        r = send_command(sock, "SET", "key1", "value1")
        assert "OK" in r or "+OK" in r
        r = send_command(sock, "GET", "key1")
        assert "value1" in r
        sock.close()
    finally:
        s.stop()

def test_get_nonexistent():
    s = RedisServer(host='127.0.0.1', port=16381, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16381))
        r = send_command(sock, "GET", "no_key")
        assert "$-1" in r or "null" in r or "nil" in r.lower()
        sock.close()
    finally:
        s.stop()

def test_del():
    s = RedisServer(host='127.0.0.1', port=16382, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16382))
        send_command(sock, "SET", "temp", "val")
        r = send_command(sock, "DEL", "temp")
        assert "1" in r  # 1 key deleted
        r = send_command(sock, "GET", "temp")
        assert "$-1" in r
        sock.close()
    finally:
        s.stop()

def test_incr_decr():
    s = RedisServer(host='127.0.0.1', port=16383, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16383))
        send_command(sock, "SET", "counter", "10")
        r = send_command(sock, "INCR", "counter")
        assert "11" in r
        r = send_command(sock, "DECR", "counter")
        assert "10" in r
        sock.close()
    finally:
        s.stop()

def test_expire_ttl():
    s = RedisServer(host='127.0.0.1', port=16384, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16384))
        send_command(sock, "SET", "ephemeral", "data")
        send_command(sock, "EXPIRE", "ephemeral", "1")
        r = send_command(sock, "TTL", "ephemeral")
        assert int(r.strip().lstrip(":").strip()) > 0
        time.sleep(1.5)
        r = send_command(sock, "GET", "ephemeral")
        assert "$-1" in r
        sock.close()
    finally:
        s.stop()

def test_keys_pattern():
    s = RedisServer(host='127.0.0.1', port=16385, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16385))
        send_command(sock, "SET", "user:1", "alice")
        send_command(sock, "SET", "user:2", "bob")
        send_command(sock, "SET", "post:1", "hello")
        r = send_command(sock, "KEYS", "user:*")
        assert "user:1" in r and "user:2" in r and "post:1" not in r
        sock.close()
    finally:
        s.stop()

def test_list_operations():
    s = RedisServer(host='127.0.0.1', port=16386, data_dir=DATA_DIR)
    t = threading.Thread(target=s.start, daemon=True)
    t.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16386))
        send_command(sock, "LPUSH", "mylist", "c")
        send_command(sock, "LPUSH", "mylist", "b")
        send_command(sock, "LPUSH", "mylist", "a")
        r = send_command(sock, "LLEN", "mylist")
        assert "3" in r
        r = send_command(sock, "LPOP", "mylist")
        assert "a" in r
        r = send_command(sock, "RPOP", "mylist")
        assert "c" in r
        sock.close()
    finally:
        s.stop()

def test_aof_persistence():
    data_dir2 = DATA_DIR + "_aof"
    if os.path.exists(data_dir2):
        shutil.rmtree(data_dir2)
    os.makedirs(data_dir2, exist_ok=True)

    s1 = RedisServer(host='127.0.0.1', port=16387, data_dir=data_dir2)
    t1 = threading.Thread(target=s1.start, daemon=True)
    t1.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16387))
        send_command(sock, "SET", "persist", "forever")
        sock.close()
    finally:
        s1.stop()
        time.sleep(0.2)

    # Restart and check data
    s2 = RedisServer(host='127.0.0.1', port=16387, data_dir=data_dir2)
    t2 = threading.Thread(target=s2.start, daemon=True)
    t2.start()
    time.sleep(0.2)
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.connect(('127.0.0.1', 16387))
        r = send_command(sock, "GET", "persist")
        assert "forever" in r
        sock.close()
    finally:
        s2.stop()
        shutil.rmtree(data_dir2, ignore_errors=True)

PYEOF
# pytest isn't imported by default in the test file, let's check
if ! grep -q "import pytest" /tmp/repos/python_04_mini_redis/tests/test_server.py 2>/dev/null; then
    # Add pytest import and fixture marker
    true
fi
echo "python_04_mini_redis setup done"
