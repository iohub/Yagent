# 关键词词典匹配系统设计

## 1. 设计背景

### 1.1 问题陈述
- 现有系统仅有 TUI 自动补全用的简单关键词前缀匹配
- 缺少统一的词典匹配引擎
- 无法支持全文内容扫描、白名单/黑名单等高级场景
- 缺少热重载机制

### 1.2 设计目标
- 支持多种词典类型（前缀补全、精确扫描、白名单、黑名单）
- 使用成熟的 Aho-Corasick 算法实现高效多模式匹配
- 支持用户自定义词典和内置默认词典
- 支持热重载
- 保持向后兼容
- 并发安全

## 2. 算法选型对比

### 2.1 候选方案

| 算法 | 预处理时间 | 匹配速度 | 内存占用 | 实现复杂度 | 适用场景 |
|------|-----------|---------|---------|-----------|---------|
| Aho-Corasick | O(m×k) | O(n) | 较高 | 中 | 多模式精确匹配 |
| Rabin-Karp (多模) | O(m) | 平均 O(n×min) | 低 | 简单 | 较少用 |
| 排序数组 + 二分查找 | O(n log n) | O(log n + k) | 低 | 极低 | 前缀匹配/补全 |
| Trie | O(m) | O(n×L) | 中 | 中 | 前缀匹配、补全 |
| Hyperscan (C) | 极快 | 极快 | 适中 | 高 | 需要 cgo，性能极致 |

**说明**：m = 所有关键词总字符数，n = 文本长度，k = 匹配数，L = 最长关键词长度，K = 关键词数量

### 2.2 最终选择

采用**混合方案**：
- **前缀匹配（补全）**：排序数组 + 二分查找，复杂度 O(log K + k)，实现极简，适合少量关键词的快速前缀搜索
- **精确匹配（扫描）**：Aho-Corasick 算法，O(n+m+z) 线性时间，适合大量关键词的全文扫描

选择 Aho-Corasick 的理由：
1. 纯 Go 实现（`github.com/anknown/ahocorasick`），无 CGO 依赖
2. 性能优异：O(n) 匹配时间，适合 10k+ 关键词扫描大文件
3. 成熟可靠：广泛使用于入侵检测、文本过滤等场景
4. 未来可扩展：接口设计支持替换后端

## 3. 系统架构

### 3.1 整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    关键词词典匹配系统                      │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  config/config.toml                                     │
│  ┌─────────────┐   ┌──────────────┐   ┌─────────────┐  │
│  │  [keywords]  │──▶│  dict.Manager│──▶│  AutoComplete│ │
│  │  default_   │   │   (管理器)    │   │  (补全词典)  │  │
│  │  path       │   ├──────────────┤   └─────────────┘  │
│  │  hot_reload │   │  completion  │                     │
│  │  [[dict]]   │   │  .Dict       │   ┌─────────────┐  │
│  └─────────────┘   │  (补全)      │──▶│  Scanner     │  │
│                    └──────────────┘   │  Matcher     │  │
│                                       │  (AC扫描器)  │  │
│                    ┌──────────────┐   │              │  │
│  文件源:            │ fsnotify     │   └─────────────┘  │
│  • 用户级:          │  (热重载)    │                     │
│    ~/.codeactor/    └──────────────┘   ┌─────────────┐  │
│    keywords.txt  │                     │ whitelist.txt│  │
│  • 项目级:        │                     │ blacklist.txt│  │
│    .codeactor/    │                     │  (用户自定义) │  │
│    keywords.txt   │                     └─────────────┘  │
│  • 内置默认:      │                                     │
│    defaults_kw.txt│                                     │
│    (213 个编程词汇) │                                     │
└─────────────────────────────────────────────────────────┘
```

### 3.2 组件关系

- **Manager（管理器）**：顶层协调者，根据配置创建和管理所有词典实例
- **CompletionDict（补全词典）**：实现 `CompletionProvider` 接口，使用前缀匹配
- **ScannerMatcher（扫描器）**：实现 `Matcher` 接口，使用 Aho-Corasick 算法
- **fsnotify（热重载）**：监控文件变更，触发字典重载

### 3.3 数据流

1. 系统启动时，`Manager` 读取 `config.toml` 中的 `[keywords]` 配置
2. 根据配置创建对应的词典实例（补全词典或扫描器）
3. 若未配置，则自动创建默认的 `autocomplete` 词典（向后兼容）
4. 运行时，通过 `Manager.AutoComplete()` 或 `Manager.MatchAll()` 查询

## 4. 配置设计

### 4.1 配置文件结构

```toml
# ── 关键词词典配置 ──
[keywords]
default_path = "~/.codeactor/keywords.txt"   # 默认关键词文件路径
hot_reload = true                             # 是否启用热重载

# 自动补全词典
[[dict]]
name = "autocomplete"
files = []                                    # 留空表示使用 default_path 和项目级文件
type = "prefix"                               # 前缀匹配
builtin_type = "default"                      # 使用内置默认关键词

# 白名单词典（精确匹配）
# [[dict]]
# name = "whitelist"
# files = [".codeactor/whitelist.txt"]
# type = "exact"
# builtin_type = "none"

# 黑名单词典（精确匹配）
# [[dict]]
# name = "blacklist"
# files = [".codeactor/blacklist.txt"]
# type = "exact"
# builtin_type = "none"
```

### 4.2 配置结构体（Go 代码）

```go
// DictTypePrefix - 前缀匹配
const (
	DictTypePrefix = "prefix"
	DictTypeExact  = "exact"
)

// DictConfig 词典配置项
type DictConfig struct {
	Name        string   `toml:"name"`
	Files       []string `toml:"files"`
	Type        string   `toml:"type"`          // "prefix" or "exact"
	BuiltinType string   `toml:"builtin_type"`  // "default", "none"
}

// KeywordsConfig 关键词词典配置
type KeywordsConfig struct {
	DefaultPath string       `toml:"default_path"`
	HotReload   bool         `toml:"hot_reload"`
	Dicts       []DictConfig `toml:"dict"`
}
```

### 4.3 向后兼容

如果 `config.toml` 中不存在 `[keywords]` 段，系统自动创建默认配置：
- `DefaultPath` = `~/.codeactor/keywords.txt`
- `HotReload` = `false`
- `Dicts` = 包含一个 `autocomplete` 词典（type="prefix", builtin_type="default"）

## 5. API 设计

### 5.1 核心接口

```go
// Match 表示一次匹配结果
type Match struct {
	Keyword  string
	Start    int
	End      int
	DictName string
}

// Matcher 扫描器匹配器通用接口
type Matcher interface {
	MatchAll(text []byte) []Match
	Name() string
	Reload() error
}

// CompletionProvider 补全专用接口
type CompletionProvider interface {
	Complete(prefix string) []string
	Name() string
	Reload() error
}

// Manager 词典管理器接口
type Manager interface {
	AutoComplete(prefix string) []string
	MatchAll(dictName string, text []byte) ([]Match, error)
	ListDicts() []string
	Close() error
}
```

### 5.2 使用示例

#### 5.2.1 自动补全
```go
manager, err := dict.NewManager(config, defaultPath)
// ...
suggestions := manager.AutoComplete("con")
// 返回 ["config", "context", "controller", ...]
```

#### 5.2.2 内容扫描
```go
matches, err := manager.MatchAll("whitelist", codeBytes)
for _, m := range matches {
	fmt.Printf("匹配: %s at [%d:%d]\n", m.Keyword, m.Start, m.End)
}
```

#### 5.2.3 创建词典管理器
```go
cfg := config.KeywordsConfig
defaultPath := "~/.codeactor/keywords.txt"
manager, err := dict.NewManager(cfg, defaultPath)
if err != nil {
	log.Fatalf("Failed to create keyword manager: %v", err)
}
defer manager.Close()
```

## 6. 实现细节

### 6.1 补全词典（CompletionDict）

- **数据结构**：排序字符串切片 + `sync.RWMutex`
- **插入方式**：`AddWords()` 使用二分插入保持有序
- **匹配算法**：`sort.Search()` 定位起点，顺序扫描前缀匹配
- **时间复杂度**：O(log K + k)，K=关键词数，k=匹配数
- **并发安全**：`atomic.Value` 存储数据，无锁读

### 6.2 AC 扫描器（ScannerMatcher）

- **算法**：Aho-Corasick
- **库依赖**：`github.com/anknown/ahocorasick`（纯 Go 实现）
- **数据结构**：Trie + Fail 指针 + 输出表
- **匹配算法**：状态机驱动，O(n) 线性扫描
- **并发安全**：`atomic.Value` 存储 Trie，原子替换

### 6.3 热重载机制

```go
func (m *ManagerImpl) handleFileEvent(event fsnotify.Event) {
	// 1. 过滤临时文件（Emacs backup, Vim swap 等）
	if isTempFile(event.Name) {
		return
	}

	// 2. 根据事件类型处理
	switch event.Op {
	case fsnotify.Write:
		triggerReload(event.Name)
	case fsnotify.Rename, fsnotify.Remove:
		// 编辑器原子写入：先 rename 再 write
		// 移除旧监控，重新添加
	}

	// 3. 原子替换词典数据
	dict.Reload() // 使用 atomic.Value.Store 替换
}
```

**关键设计**：
- 使用 `atomic.Bool` 防止并发重复重载
- 重载时先构建新数据，再原子替换
- 读取操作无锁，通过原子值保证一致性

### 6.4 内置词典

- **文件格式**：纯文本，每行一个关键词，`#` 开头为注释
- **嵌入方式**：`//go:embed defaults_kw.txt`
- **默认关键词**：213 个常见编程词汇（agent, api, config, server 等）

## 7. 目录结构

```
internal/dict/
├── engine.go         # 接口定义（Match, Matcher, CompletionProvider, Manager）
├── completion.go     # CompletionDict（补全词典，排序数组 + 二分查找）
├── scanner.go        # ScannerMatcher（AC 扫描器，Aho-Corasick 算法）
├── manager.go        # ManagerImpl（词典管理器 + 热重载）
├── defaults.go       # 内置关键词（//go:embed 嵌入）
├── defaults_kw.txt   # 213 个默认编程关键词
└── dict_test.go      # 全面单元测试（9 组测试，含 race 检测）
```

## 8. 性能指标

### 8.1 补全词典
- 关键词数量：~200（默认）
- 单次 Complete 调用：< 1μs
- 内存占用：~10KB

### 8.2 AC 扫描器
- 关键词数量：10,000+
- 匹配速度：1MB 文本 < 10ms
- 内存占用：关键词总字符数的 5~10 倍

### 8.3 并发性能
- 100 goroutines × 100 次调用：无竞态条件（经 `-race` 检测）

## 9. 测试策略

### 9.1 单元测试
- **CompletionDict 测试**：前缀匹配、文件加载、内置关键词、并发安全
- **ScannerMatcher 测试**：精确匹配、Unicode 支持、重叠匹配、并发安全
- **Manager 测试**：配置加载、向后兼容、热重载、基本功能

### 9.2 测试覆盖
- 边界情况：空列表、空文本、超长关键词、特殊字符
- Unicode：中文、Emoji、混合字符
- 并发：`go test -race` 检测
- 热重载：模拟文件变更，验证匹配结果更新

## 10. 扩展性设计

### 10.1 未来新增词典类型
只需在配置文件中添加新的 `[[dict]]` 条目：
```toml
[[dict]]
name = "syntax_highlight"
files = [".codeactor/syntax.txt"]
type = "exact"
builtin_type = "none"
```

系统会自动创建对应的 `ScannerMatcher` 实例。

### 10.2 算法替换
- 补全词典可替换为 Trie 实现
- AC 扫描器可替换为 Hyperscan（需要 CGO）
- 接口不变，只需实现 `Matcher` 或 `CompletionProvider`

## 11. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 向后兼容破坏 | 高 | 默认配置自动创建 autocomplete 词典 |
| 热重载竞态 | 中 | `atomic.Value` 原子替换 |
| 内存占用 | 低 | 10k 关键词 < 1MB |
| CGO 依赖 | 低 | 选择纯 Go 库 |
| 测试遗漏 | 中 | 全面的单元测试 + race detector |

## 12. 总结

本设计采用**渐进式抽象 + Aho-Corasick + 原子热重载**方案：
- ✅ 支持多种词典类型（前缀/精确/白名单/黑名单）
- ✅ 使用成熟的 Aho-Corasick 算法
- ✅ 支持热重载
- ✅ 保持 100% 向后兼容
- ✅ 并发安全（经 `-race` 检测）
- ✅ 全面的测试覆盖

---

*文档版本：v1.0*
*创建时间：2024*
*最后更新：2024*
