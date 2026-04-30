# 任务：实现装饰器依赖注入框架

请实现一个基于装饰器的 Python 依赖注入（DI）框架。

## 功能要求

1. **Container 类** — 核心 IoC 容器
   - `register(name_or_type, instance_or_class)` — 注册依赖
   - `resolve(name_or_type, default=None)` — 解析依赖
   - `register_provider(typ, factory_fn)` — 注册工厂函数
   - `register_factory(name, factory_fn)` — 注册工厂（每次返回新实例）
   - `register_scoped(typ)` — 注册作用域依赖
   - `scope()` — 创建子作用域上下文管理器

2. **@inject 装饰器** — 标记构造函数需要自动注入依赖
   - 自动从容器解析参数类型注解对应的实例

3. **@provide 装饰器** — 标记函数为依赖提供者

4. **@singleton 装饰器** — 标记类为单例模式
   - 容器始终返回同一个实例

5. **循环依赖检测** — 解析时检测到循环依赖应抛出异常

## 验收标准

运行 `python3 -m pytest tests/ -v` 所有 8 个测试通过即完成任务。
