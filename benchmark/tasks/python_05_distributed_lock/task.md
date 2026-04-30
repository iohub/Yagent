# 任务：实现分布式锁管理器

请实现一个基于文件锁的分布式锁管理器，模拟分布式系统中的锁机制。

## 功能要求

1. **LockManager 类**
   - `acquire(lock_name, ttl_seconds, owner_id) -> bool` — 获取锁，支持 TTL
   - `release(lock_name, owner_id) -> bool` — 释放锁（仅 owner 可释放）
   - `renew(lock_name, ttl_seconds, owner_id) -> bool` — 续约锁的 TTL
   - `get_owner(lock_name) -> str` — 获取当前锁持有者
   - `get_ttl(lock_name) -> float` — 获取锁的剩余 TTL
   - `wait_for_lock(lock_name, timeout, ttl_seconds) -> bool` — 等待锁可用（阻塞）

2. **Lock 上下文管理器**
   - `with Lock(mgr, name, ttl)` — 自动获取和释放

3. **核心机制**
   - **TTL 过期** — 锁达到 TTL 后自动释放
   - **续约** — 持有者可在 TTL 到期前续约
   - **所有权验证** — 只有持有者可以释放或续约
   - **死锁检测** — 持有者终止后锁自动过期（基于 TTL）
   - **并发竞争** — 多进程/线程不能同时持有同一把锁

## 技术约束

- 使用文件系统实现（`fcntl.flock` 或基于目录的文件标记）
- 锁状态持久化在文件中
- TTL 通过文件时间戳判断

## 验收标准

运行 `python3 -m pytest tests/ -v` 所有 10 个测试通过即完成任务。
