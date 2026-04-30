# 任务：实现线程安全的 LRU 缓存

请实现一个泛型线程安全的 LRU（最近最少使用）缓存 `LruCache<K, V>`。

## 功能要求

1. **构造函数** `new(capacity: usize)` — 创建指定容量的缓存
2. **get(&K) -> Option<&V>** — 获取值并将该键标记为最近使用
3. **put(K, V)** — 插入键值对，若超出容量则淘汰最近最少使用的条目
4. **put_with_ttl(K, V, Duration)** — 插入带过期时间的条目
5. **remove(&K)** — 删除指定键
6. **clear()** — 清空缓存
7. **len() -> usize** — 返回当前条目数
8. **contains(&K) -> bool** — 检查键是否存在
9. **线程安全** — 使用 `Arc<Mutex<>>` 或类似机制保证多线程安全访问

## 技术约束

- 必须使用 Rust 标准库实现（可依赖 `std::sync` 和 `std::collections`）
- K 必须实现 `Eq + Hash + Clone`
- V 必须实现 `Clone`
- TTL 过期条目在 `get` 时自动清理
- 淘汰策略严格按照 LRU 顺序

## 验收标准

项目已包含完整的测试文件 `tests/lru_test.rs`，运行 `cargo test` 所有测试通过即完成任务。
