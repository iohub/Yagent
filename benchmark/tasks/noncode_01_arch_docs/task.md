# 任务：分析项目架构并生成架构文档

请完整分析这个 Go Web 项目的代码架构，生成一份详细的架构文档保存为 `ARCHITECTURE.md`。

## 分析要求

1. **项目概述** — 项目名称、用途、技术栈（Go + SQLite + JWT）
2. **目录结构说明** — 每个目录和关键文件的职责
3. **模块职责分析**：
   - `cmd/server/main.go` — 入口点和依赖组装
   - `internal/handler/` — HTTP 处理器层（UserHandler, OrderHandler）
   - `internal/service/` — 业务逻辑层（UserService, OrderService）
   - `internal/repo/` — 数据访问层（UserRepo, OrderRepo, DB）
   - `internal/middleware/` — 认证中间件（AuthMiddleware）
   - `pkg/validator/` — 工具包（邮箱校验）
4. **数据流图**（文字描述） — 从 HTTP 请求到数据库查询的完整链路
5. **组件依赖关系** — 各模块之间的依赖关系，分层架构说明
6. **关键设计决策** — 分析代码中体现的设计模式（分层架构、依赖注入、Repository 模式等）
7. **安全相关** — JWT 认证流程、中间件拦截机制

## 验收标准

生成的 `ARCHITECTURE.md` 需包含以上所有方面的分析内容。
