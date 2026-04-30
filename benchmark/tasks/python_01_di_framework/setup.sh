#!/bin/bash
set -e
REPO="/tmp/repos/python_01_di_framework"
rm -rf "$REPO"
mkdir -p "$REPO/di_framework"
cd "$REPO"
touch di_framework/__init__.py

cat > di_framework/container.py << 'PYEOF'
# TODO: Implement Dependency Injection Container
class Container:
    pass
PYEOF

cat > di_framework/providers.py << 'PYEOF'
# TODO: Implement @inject, @provide, @singleton decorators
def inject(*args, **kwargs):
    def decorator(fn):
        return fn
    return decorator

def provide(fn):
    return fn

def singleton(cls):
    return cls
PYEOF

mkdir -p tests
cat > tests/test_injection.py << 'PYEOF'
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from di_framework.container import Container
from di_framework.providers import inject, provide, singleton

def test_container_register_and_resolve():
    c = Container()
    c.register("db_url", "postgres://localhost/test")
    assert c.resolve("db_url") == "postgres://localhost/test"

def test_provide_registers_type():
    class Database:
        def __init__(self, url: str): self.url = url

    c = Container()
    c.register("db_url", "postgres://localhost/test")
    c.register(Database, Database)

    @provide
    def make_db(url: str) -> Database:
        return Database(url)

    c.register_provider(Database, make_db)
    db = c.resolve(Database)
    assert isinstance(db, Database)
    assert db.url == "postgres://localhost/test"

def test_inject_decorator():
    class Logger:
        def log(self, msg): return f"[LOG] {msg}"

    class UserService:
        @inject
        def __init__(self, logger: Logger):
            self.logger = logger

    c = Container()
    c.register(Logger, Logger())
    c.register(UserService, UserService)
    svc = c.resolve(UserService)
    assert isinstance(svc.logger, Logger)

def test_singleton_returns_same_instance():
    @singleton
    class Config:
        def __init__(self): self.debug = True

    c = Container()
    c.register(Config, Config())
    c1 = c.resolve(Config)
    c2 = c.resolve(Config)
    assert c1 is c2

def test_scoped_dependency():
    class Request:
        pass

    class RequestHandler:
        @inject
        def __init__(self, request: Request):
            self.request = request

    c = Container()
    c.register_scoped(Request)
    c.register(RequestHandler, RequestHandler)

    with c.scope() as scoped:
        handler = scoped.resolve(RequestHandler)
        assert isinstance(handler.request, Request)

def test_circular_dependency_detection():
    class A:
        @inject
        def __init__(self, b): self.b = b
    class B:
        @inject
        def __init__(self, a): self.a = a

    c = Container()
    c.register(A, A)
    c.register(B, B)
    try:
        c.resolve(A)
        assert False, "Should raise circular dependency error"
    except Exception:
        pass

def test_resolve_with_default_value():
    c = Container()
    val = c.resolve("nonexistent", default="fallback")
    assert val == "fallback"

def test_register_factory():
    c = Container()
    counter = [0]
    def make_id():
        counter[0] += 1
        return counter[0]
    c.register_factory("id", make_id)
    assert c.resolve("id") == 1
    assert c.resolve("id") == 2

PYEOF

echo "python_01_di_framework setup done"
