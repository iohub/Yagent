# TODO

Browser-Agent 功能测试发现的问题列表。

## 🐛 Bug

### evaluate_js 返回值类型错误
- **症状**: 所有 `evaluate_js` 执行返回值（字符串、对象、数组）均触发 `TypeError: ...apply is not a function`
- **影响**: JavaScript 执行工具完全不可用于带返回值的场景
- **优先级**: 高

### 表单提交超时
- **症状**: 在 https://httpbin.org/forms/post 填写并提交表单时，Browser-Agent 超时（context deadline exceeded）
- **影响**: 表单交互类工具可能无法完整执行
- **优先级**: 中

## ⚠️ 改进项

### 滚动测试缺乏合适的目标页面
- **描述**: example.com 内容太少无法有效测试滚动功能，httpbin.org 部分页面响应慢
- **建议**: 使用内容丰富的测试页面或在 testhelpers 中增加可滚动测试路由

## ✅ 已验证通过

| 功能 | 状态 |
|------|------|
| 导航（navigate） | ✅ |
| 前进/后退（back/forward） | ✅ |
| Cookie 管理（get/set） | ✅ |
| 截图（screenshot） | ✅ |
| PDF 生成 | ✅ |
| 文本提取（extract_text） | ✅ |
| HTML 提取 | ✅ |
