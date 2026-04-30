# 任务：实现简易模板引擎

请实现一个支持变量替换、条件判断和循环的文本模板引擎。

## 功能要求

1. **变量替换** `{{variable_name}}` — 替换为上下文中的变量值
2. **HTML 转义** — 变量值中的 HTML 特殊字符（`<`, `>`, `&`, `"`, `'`）自动转义
3. **条件判断** `{% if var %}...{% else %}...{% endif %}` — 根据变量真值显示不同内容（"true" 为真，"false" 或空为假）
4. **循环** `{% for item in list %}...{{item}}...{% endfor %}` — 遍历列表
5. **嵌套结构** — 支持 if 内嵌套 for
6. **错误处理** — 未闭合标签、未定义变量等返回 `Err(String)`

## 数据结构

```rust
pub struct Context {
    pub vars: HashMap<String, String>,
}
impl Context {
    pub fn set_list(&mut self, key: &str, items: Vec<String>) { ... }
}

pub struct Template;
impl Template {
    pub fn new(template: &str) -> Result<Self, String> { ... }
    pub fn render(&self, ctx: &Context) -> Result<String, String> { ... }
}
```

## 验收标准

运行 `cargo test` 所有测试通过即完成任务。
