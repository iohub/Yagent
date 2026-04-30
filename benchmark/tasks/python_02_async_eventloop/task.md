# 任务：实现微型异步事件循环

请实现一个基于协程的微型异步事件循环，支持任务调度和 I/O 多路复用。

## 功能要求

1. **EventLoop 类** — 事件循环核心
   - `run_until_complete(coro)` — 运行协程直到完成
   - 使用 `selectors` 模块实现 I/O 多路复用
   - 支持任务队列调度

2. **Task 类** — 可取消的异步任务
   - `cancel()` — 取消任务

3. **协程支持**
   - `create_task(coro)` — 创建并发任务
   - `sleep(seconds)` — 异步休眠
   - `gather(*coros)` — 并发运行多个协程

4. **异常传播** — 协程中的异常应正确传播到调用方

## 技术约束

- 使用 `selectors.DefaultSelector` 实现定时器和 I/O 等待
- 使用 Python 原生 `async/await` 语法
- 不使用 `asyncio` 标准库
- 使用 `types.coroutine` 或生成器实现协程

## 验收标准

运行 `python3 -m pytest tests/ -v` 所有 8 个测试通过即完成任务。
