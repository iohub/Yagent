# 任务：实现 Redis 兼容的 TCP 服务器

请实现一个兼容 Redis 协议的 TCP 键值存储服务器。

## 功能要求

1. **TCP 服务器** — 监听指定端口，支持多客户端并发连接
2. **Redis 协议** — 支持 RESP（REdis Serialization Protocol）简单子集

## 支持的命令

| 命令 | 功能 |
|------|------|
| `SET key value` | 设置键值 |
| `GET key` | 获取键值 |
| `DEL key` | 删除键 |
| `EXPIRE key seconds` | 设置键的过期时间 |
| `TTL key` | 查询键的剩余生存时间 |
| `INCR key` | 值加 1 |
| `DECR key` | 值减 1 |
| `KEYS pattern` | 模式匹配键（支持 `*` 通配符） |
| `LPUSH key value` | 列表头部插入 |
| `RPUSH key value` | 列表尾部插入 |
| `LPOP key` | 列表头部弹出 |
| `RPOP key` | 列表尾部弹出 |
| `LLEN key` | 列表长度 |

## 高级要求

- **AOF 持久化** — 将写命令追加到 AOF 文件，重启时重放恢复数据
- **过期清理** — 被动过期（访问时检查）+ 定期清理
- **并发安全** — 多客户端同时操作数据一致性

## 验收标准

运行 `python3 -m pytest tests/ -v` 所有 8 个测试通过即完成任务。

注意：测试文件中使用了 `pytest` fixture，需要在测试文件顶部添加 `import pytest`。
