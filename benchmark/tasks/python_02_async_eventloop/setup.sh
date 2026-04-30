#!/bin/bash
set -e
REPO="/tmp/repos/python_02_async_eventloop"
rm -rf "$REPO"
mkdir -p "$REPO/async_loop"
cd "$REPO"
touch async_loop/__init__.py

cat > async_loop/event_loop.py << 'PYEOF'
# TODO: Implement a mini async event loop
class EventLoop:
    pass

class Task:
    pass

def create_task(coro):
    return Task()

def run(coro):
    pass

def sleep(seconds):
    pass

def gather(*coros):
    pass
PYEOF

mkdir -p tests
cat > tests/test_eventloop.py << 'PYEOF'
import sys, os, time, socket, threading
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from async_loop.event_loop import EventLoop, create_task, run, sleep, gather, Task
import selectors

def test_simple_task():
    results = []
    async def my_task():
        results.append(1)
        results.append(2)
    loop = EventLoop()
    loop.run_until_complete(my_task())
    assert results == [1, 2]

def test_sleep():
    results = []
    async def delayed():
        results.append("start")
        await sleep(0.1)
        results.append("end")
    loop = EventLoop()
    start = time.time()
    loop.run_until_complete(delayed())
    elapsed = time.time() - start
    assert elapsed >= 0.09
    assert results == ["start", "end"]

def test_concurrent_tasks():
    order = []
    async def task_a():
        order.append("a1")
        await sleep(0.05)
        order.append("a2")

    async def task_b():
        order.append("b1")
        await sleep(0.02)
        order.append("b2")

    async def main():
        t1 = create_task(task_a())
        t2 = create_task(task_b())
        await t1
        await t2

    loop = EventLoop()
    loop.run_until_complete(main())
    # b should finish before a due to shorter sleep
    assert order.index("b2") < order.index("a2")

def test_gather():
    results = []
    async def work(n):
        await sleep(0.01)
        results.append(n)

    async def main():
        await gather(work(1), work(2), work(3))

    loop = EventLoop()
    loop.run_until_complete(main())
    assert sorted(results) == [1, 2, 3]

def test_exception_propagation():
    async def failing():
        await sleep(0.01)
        raise ValueError("test error")

    loop = EventLoop()
    try:
        loop.run_until_complete(failing())
        assert False, "Should have raised"
    except ValueError as e:
        assert str(e) == "test error"

def test_task_cancellation():
    cancelled = []
    async def cancel_me():
        try:
            await sleep(1.0)
        except Exception:
            cancelled.append(True)
            raise

    async def main():
        t = create_task(cancel_me())
        await sleep(0.01)
        t.cancel()
        try:
            await t
        except Exception:
            pass

    loop = EventLoop()
    loop.run_until_complete(main())
    assert len(cancelled) == 1

def test_nested_await():
    async def inner():
        return 42
    async def outer():
        v = await inner()
        return v * 2
    loop = EventLoop()
    result = loop.run_until_complete(outer())
    assert result == 84

def test_chained_sleep():
    times = []
    async def chain():
        times.append(time.time())
        await sleep(0.05)
        times.append(time.time())
        await sleep(0.05)
        times.append(time.time())

    loop = EventLoop()
    loop.run_until_complete(chain())
    assert times[1] - times[0] >= 0.04
    assert times[2] - times[1] >= 0.04

PYEOF

echo "python_02_async_eventloop setup done"
