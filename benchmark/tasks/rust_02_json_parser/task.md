# 任务：实现 JSON 解析器（子集）

请实现一个 JSON 解析器，能够将 JSON 字符串解析为 Rust 枚举类型 `JsonValue`，不依赖 serde 等外部 crate。

## 功能要求

1. 支持解析以下 JSON 类型：
   - `null` → `JsonValue::Null`
   - `true` / `false` → `JsonValue::Bool(bool)`
   - 整数和浮点数（支持负数） → `JsonValue::Number(f64)`
   - 字符串（支持 `\n`, `\t`, `\\`, `\"` 转义） → `JsonValue::String(String)`
   - Unicode 转义 `\uXXXX` → 对应字符
   - 数组（支持嵌套） → `JsonValue::Array(Vec<JsonValue>)`
   - 对象（支持嵌套） → `JsonValue::Object(HashMap<String, JsonValue>)`
2. 对格式错误的 JSON 返回 `Err(String)` 错误信息
3. 能处理嵌套结构（如 `{"items":[1,{"x":true}]}`）

## 验收标准

项目包含测试文件 `tests/json_test.rs`，运行 `cargo test` 所有 15 个测试通过即完成任务。
