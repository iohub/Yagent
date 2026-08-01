package dict

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// 1. TestCompletionDictBasic — 基本补全词典功能
// ============================================================================

func TestCompletionDictBasic(t *testing.T) {
	// 测试 NewCompletionDict 创建
	t.Run("CreateDict", func(t *testing.T) {
		d := NewCompletionDict("test", nil)
		if d == nil {
			t.Fatal("NewCompletionDict 返回 nil")
		}
		if d.Name() != "test" {
			t.Errorf("Name() = %q, 期望 %q", d.Name(), "test")
		}
		if d.WordCount() != 0 {
			t.Errorf("WordCount() = %d, 期望 0", d.WordCount())
		}
	})

	// 测试从数据加载关键词
	t.Run("LoadFromData", func(t *testing.T) {
		data := []byte("agent\napi\nconfig\nhttp\nserver")
		d := NewCompletionDict("load_test", nil)

		if err := d.LoadFromData(data); err != nil {
			t.Fatalf("LoadFromData 失败: %v", err)
		}

		expected := 5
		if got := d.WordCount(); got != expected {
			t.Errorf("WordCount() = %d, 期望 %d", got, expected)
		}
	})

	// 测试 Complete() 前缀匹配
	t.Run("CompletePrefixMatch", func(t *testing.T) {
		data := []byte("agent\napi\napple\napplication\nconfig\ncount\nhttp\nhttps")
		d := NewCompletionDict("prefix_test", nil)
		d.LoadFromData(data)

		// 无前缀匹配 - 返回前 limit (20) 个结果
		t.Run("EmptyPrefix", func(t *testing.T) {
			results := d.Complete("")
			if len(results) == 0 {
				t.Error("空前缀应返回所有关键词")
			}
			if len(results) > 20 {
				t.Errorf("返回 %d 个结果，最多应为 20", len(results))
			}
		})

		// 单个前缀匹配
		t.Run("SinglePrefix", func(t *testing.T) {
			results := d.Complete("api")
			if len(results) == 0 {
				t.Error("应有匹配结果")
			}
			found := false
			for _, r := range results {
				if r == "api" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("未找到 'api'，结果为: %v", results)
			}
		})

		// 多个前缀匹配
		t.Run("MultiplePrefix", func(t *testing.T) {
			results := d.Complete("ap")
			if len(results) < 3 {
				t.Errorf("前缀 'ap' 应匹配至少 3 个词，得到: %v", results)
			}
			for _, r := range results {
				if !strings.HasPrefix(strings.ToLower(r), "ap") {
					t.Errorf("结果 %q 不以前缀 'ap' 开头", r)
				}
			}
		})

		// 大小写不敏感匹配
		t.Run("CaseInsensitive", func(t *testing.T) {
			resultsUpper := d.Complete("API")
			resultsMixed := d.Complete("ApI")
			resultsLower := d.Complete("api")

			if len(resultsUpper) == 0 {
				t.Error("大写前缀 'API' 应匹配")
			}
			if len(resultsMixed) == 0 {
				t.Error("混合大小写前缀 'ApI' 应匹配")
			}

			// 结果数量应该相同
			if len(resultsUpper) != len(resultsLower) {
				t.Errorf("大写前缀返回 %d 个，小写返回 %d 个", len(resultsUpper), len(resultsLower))
			}
		})

		// 匹配到自身
		t.Run("MatchSelf", func(t *testing.T) {
			results := d.Complete("agent")
			if len(results) == 0 {
				t.Error("关键词应匹配到自身")
			}
			if results[0] != "agent" {
				t.Errorf("第一个结果 = %q, 期望 'agent'", results[0])
			}
		})

		// 无匹配
		t.Run("NoMatch", func(t *testing.T) {
			results := d.Complete("zzzznonexistent")
			if len(results) != 0 {
				t.Errorf("无前缀匹配应返回空列表，得到: %v", results)
			}
		})
	})

	// 测试 WordCount()
	t.Run("WordCount", func(t *testing.T) {
		d := NewCompletionDict("count_test", nil)

		if d.WordCount() != 0 {
			t.Errorf("新词典 WordCount() = %d, 期望 0", d.WordCount())
		}

		d.AddWords([]string{"a", "b", "c"})
		if d.WordCount() != 3 {
			t.Errorf("添加 3 个词后 WordCount() = %d, 期望 3", d.WordCount())
		}

		// 重复词应去重
		d.AddWords([]string{"b", "c", "d"})
		if d.WordCount() != 4 {
			t.Errorf("添加重复词后 WordCount() = %d, 期望 4", d.WordCount())
		}
	})

	// 测试 Name()
	t.Run("Name", func(t *testing.T) {
		d := NewCompletionDict("my_dict", nil)
		if d.Name() != "my_dict" {
			t.Errorf("Name() = %q, 期望 %q", d.Name(), "my_dict")
		}
	})

	// 测试 Clear()
	t.Run("Clear", func(t *testing.T) {
		data := []byte("agent\napi\nconfig")
		d := NewCompletionDict("clear_test", nil)
		d.LoadFromData(data)

		if d.WordCount() == 0 {
			t.Error("加载后应有关键词")
		}

		d.Clear()
		if d.WordCount() != 0 {
			t.Errorf("Clear() 后 WordCount() = %d, 期望 0", d.WordCount())
		}
		// Clear() 后 Complete() 应返回空列表
		results := d.Complete("a")
		if len(results) != 0 {
			t.Errorf("Clear() 后 Complete() 应返回空列表，得到: %v", results)
		}
	})
}

// ============================================================================
// 2. TestCompletionDictFromFile — 文件加载
// ============================================================================

func TestCompletionDictFromFile(t *testing.T) {
	// 创建临时目录
	tmpDir := t.TempDir()

	// 测试包含注释行和空行的文件
	t.Run("LoadFileWithComments", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "keywords.txt")
		content := `# 这是注释
agent
api

# 另一个注释
config

http`

		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("创建临时文件失败: %v", err)
		}

		d := NewCompletionDict("file_test", nil)
		if err := d.LoadFromFile(filePath); err != nil {
			t.Fatalf("LoadFromFile 失败: %v", err)
		}

		// 应加载 4 个关键词 (agent, api, config, http)
		expected := 4
		if got := d.WordCount(); got != expected {
			t.Errorf("WordCount() = %d, 期望 %d", got, expected)
		}

		// 验证具体关键词
		results := d.Complete("a")
		found := false
		for _, r := range results {
			if r == "agent" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("未找到 'agent'，结果为: %v", results)
		}
	})

	// 测试 LoadFromSources 创建时加载
	t.Run("CreateWithSources", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "builtin.txt")
		content := "agent\napi\nhttp\nserver"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("创建临时文件失败: %v", err)
		}

		d := NewCompletionDict("sources_test", []string{filePath})
		if d == nil {
			t.Fatal("NewCompletionDict 返回 nil")
		}

		if d.WordCount() != 4 {
			t.Errorf("WordCount() = %d, 期望 4", d.WordCount())
		}
	})

	// 测试文件不存在时不 panic
	t.Run("FileNotExist", func(t *testing.T) {
		d := NewCompletionDict("notexist_test", nil)

		// LoadFromFile 应返回错误
		err := d.LoadFromFile("/nonexistent/path/to/file.txt")
		if err == nil {
			t.Error("LoadFromFile 对不存在的文件应返回错误")
		}

		// NewCompletionDict 对不存在的 source 不应 panic
		d2 := NewCompletionDict("notexist2_test", []string{"/nonexistent/path.txt"})
		if d2 == nil {
			t.Fatal("NewCompletionDict 不应返回 nil")
		}
		// 应创建空词典
		if d2.WordCount() != 0 {
			t.Errorf("WordCount() = %d, 期望 0", d2.WordCount())
		}
	})

	// 测试从多个源加载
	t.Run("MultipleSources", func(t *testing.T) {
		file1 := filepath.Join(tmpDir, "file1.txt")
		file2 := filepath.Join(tmpDir, "file2.txt")

		os.WriteFile(file1, []byte("agent\napi"), 0644)
		os.WriteFile(file2, []byte("config\nhttp"), 0644)

		d := NewCompletionDict("multi_test", []string{file1, file2})
		if d.WordCount() != 4 {
			t.Errorf("WordCount() = %d, 期望 4", d.WordCount())
		}
	})

	// 测试文件中有特殊前缀和空格
	t.Run("FileWithSpecialChars", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "special.txt")
		content := "  agent  \n  api  \nhttp"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("创建临时文件失败: %v", err)
		}

		d := NewCompletionDict("special_test", nil)
		if err := d.LoadFromFile(filePath); err != nil {
			t.Fatalf("LoadFromFile 失败: %v", err)
		}

		// 关键词应被 trim
		if d.WordCount() != 3 {
			t.Errorf("WordCount() = %d, 期望 3", d.WordCount())
		}
	})
}

// ============================================================================
// 3. TestCompletionDictBuiltin — 内置关键词
// ============================================================================

func TestCompletionDictBuiltin(t *testing.T) {
	// 测试 LoadBuiltin 加载默认关键词（实际使用 DefaultKeywords）
	t.Run("DefaultKeywords", func(t *testing.T) {
		kw := DefaultKeywords()
		if len(kw) == 0 {
			t.Fatal("默认关键词列表不应为空")
		}
	})

	// 验证默认关键词数量（至少 20 个）
	t.Run("KeywordCount", func(t *testing.T) {
		kw := DefaultKeywords()
		if len(kw) < 20 {
			t.Errorf("默认关键词数量 = %d, 期望至少 20", len(kw))
		}
		t.Logf("默认关键词数量: %d", len(kw))
	})

	// 验证常见编程词汇存在
	t.Run("CommonWordsExist", func(t *testing.T) {
		kw := DefaultKeywords()
		kwSet := make(map[string]bool)
		for _, w := range kw {
			kwSet[w] = true
		}

		commonWords := []string{"python", "javascript", "component", "websocket", "middleware", "router", "request", "database", "postgresql", "redis", "migration"}
		for _, word := range commonWords {
			if !kwSet[word] {
				t.Errorf("默认关键词中缺少 %q", word)
			}
		}
	})

	// 测试使用内置关键词创建词典
	t.Run("CreateWithBuiltin", func(t *testing.T) {
		d := NewCompletionDict("builtin_test", nil)
		d.AddWords(DefaultKeywords())

		if d.WordCount() < 20 {
			t.Errorf("WordCount() = %d, 期望至少 20", d.WordCount())
		}

		// 测试补全
		results := d.Complete("python")
		if len(results) == 0 {
			t.Error("应能匹配到 'python'")
		}

		results = d.Complete("webpack")
		if len(results) == 0 {
			t.Error("应能匹配到 'webpack'")
		}
	})
}

// ============================================================================
// 4. TestScannerMatcherBasic — AC 扫描器基本功能
// ============================================================================

func TestScannerMatcherBasic(t *testing.T) {
	// 测试 NewScannerMatcher 创建
	t.Run("Create", func(t *testing.T) {
		m, err := NewScannerMatcher("test", []string{"agent,api,config"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}
		if m == nil {
			t.Fatal("NewScannerMatcher 返回 nil")
		}
		if m.Name() != "test" {
			t.Errorf("Name() = %q, 期望 %q", m.Name(), "test")
		}
	})

	// 测试 MatchAll 基本匹配
	t.Run("MatchAll", func(t *testing.T) {
		m, err := NewScannerMatcher("match_test", []string{"agent,api,config"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		// 单个关键词匹配
		t.Run("SingleMatch", func(t *testing.T) {
			text := []byte("this is an agent")
			matches := m.MatchAll(text)
			if len(matches) != 1 {
				t.Errorf("匹配数 = %d, 期望 1", len(matches))
			}
			if len(matches) > 0 && matches[0].Keyword != "agent" {
				t.Errorf("Keyword = %q, 期望 'agent'", matches[0].Keyword)
			}
		})

		// 多个关键词匹配
		t.Run("MultipleMatch", func(t *testing.T) {
			text := []byte("agent and api and config")
			matches := m.MatchAll(text)
			if len(matches) != 3 {
				t.Errorf("匹配数 = %d, 期望 3", len(matches))
			}
		})

		// 无匹配
		t.Run("NoMatch", func(t *testing.T) {
			text := []byte("no keywords here zzzz")
			matches := m.MatchAll(text)
			if len(matches) != 0 {
				t.Errorf("匹配数 = %d, 期望 0", len(matches))
			}
		})

		// 关键词在文本中出现多次
		t.Run("MultipleOccurrences", func(t *testing.T) {
			m2, err := NewScannerMatcher("multi_occ", []string{"test,hello"})
			if err != nil {
				t.Fatalf("NewScannerMatcher 失败: %v", err)
			}
			text := []byte("test test test")
			matches := m2.MatchAll(text)
			// 注意：当前实现只返回第一次出现
			if len(matches) != 1 {
				t.Errorf("匹配数 = %d, 期望 1 (只返回首次出现)", len(matches))
			}
		})

		// 重叠关键词
		t.Run("OverlappingKeywords", func(t *testing.T) {
			m3, err := NewScannerMatcher("overlap", []string{"cat,catch,category"})
			if err != nil {
				t.Fatalf("NewScannerMatcher 失败: %v", err)
			}
			text := []byte("I caught a catcatch category")
			matches := m3.MatchAll(text)
			if len(matches) == 0 {
				t.Error("应有匹配结果")
			}

			// 验证匹配的词
			keywords := make(map[string]bool)
			for _, m := range matches {
				keywords[m.Keyword] = true
			}

			if !keywords["cat"] {
				t.Error("应匹配到 'cat'")
			}
			if !keywords["catch"] {
				t.Error("应匹配到 'catch'")
			}
			if !keywords["category"] {
				t.Error("应匹配到 'category'")
			}
		})

		// 验证 Match.Start 和 Match.End 位置正确
		t.Run("PositionCorrect", func(t *testing.T) {
			m, err := NewScannerMatcher("pos_test", []string{"hello,world"})
			if err != nil {
				t.Fatalf("NewScannerMatcher 失败: %v", err)
			}
			text := []byte("say hello world")
			matches := m.MatchAll(text)
			// 关键词 "hello" 和 "world" 都会匹配到
			if len(matches) != 2 {
				t.Fatalf("匹配数 = %d, 期望 2", len(matches))
			}
			// 验证第一个匹配 (hello)
			if matches[0].Keyword != "hello" {
				t.Errorf("第一个 Keyword = %q, 期望 'hello'", matches[0].Keyword)
			}
			if matches[0].Start != 4 {
				t.Errorf("Start = %d, 期望 4", matches[0].Start)
			}
			if matches[0].End != 9 {
				t.Errorf("End = %d, 期望 9", matches[0].End)
			}
			// 验证第二个匹配 (world)
			if matches[1].Keyword != "world" {
				t.Errorf("第二个 Keyword = %q, 期望 'world'", matches[1].Keyword)
			}
			if matches[1].Start != 10 {
				t.Errorf("world Start = %d, 期望 10", matches[1].Start)
			}
			if matches[1].End != 15 {
				t.Errorf("world End = %d, 期望 15", matches[1].End)
			}
		})

		// 验证 Match.Keyword 内容正确
		t.Run("KeywordContent", func(t *testing.T) {
			m, err := NewScannerMatcher("kw_test", []string{"config,other"})
			if err != nil {
				t.Fatalf("NewScannerMatcher 失败: %v", err)
			}
			text := []byte("loading config file")
			matches := m.MatchAll(text)
			if len(matches) != 1 {
				t.Fatalf("匹配数 = %d, 期望 1", len(matches))
			}
			if matches[0].Keyword != "config" {
				t.Errorf("Keyword = %q, 期望 'config'", matches[0].Keyword)
			}
		})

		// 验证 Match.DictName 正确
		t.Run("DictName", func(t *testing.T) {
			m, err := NewScannerMatcher("dict_name_test", []string{"test,hello"})
			if err != nil {
				t.Fatalf("NewScannerMatcher 失败: %v", err)
			}
			text := []byte("this is a test")
			matches := m.MatchAll(text)
			if len(matches) != 1 {
				t.Fatalf("匹配数 = %d, 期望 1", len(matches))
			}
			if matches[0].DictName != "dict_name_test" {
				t.Errorf("DictName = %q, 期望 'dict_name_test'", matches[0].DictName)
			}
		})
	})

	// 测试 Reload
	t.Run("Reload", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "kw.txt")

		// 初始加载
		os.WriteFile(filePath, []byte("agent"), 0644)
		m, err := NewScannerMatcher("reload_test", []string{filePath})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("this is an agent")
		matches := m.MatchAll(text)
		if len(matches) != 1 {
			t.Error("初始加载应匹配 'agent'")
		}

		// 修改文件
		os.WriteFile(filePath, []byte("config"), 0644)

		// 重载
		if err := m.Reload(); err != nil {
			t.Fatalf("Reload 失败: %v", err)
		}

		// 验证新内容
		matches = m.MatchAll(text)
		if len(matches) != 0 {
			t.Errorf("重载后不应匹配 'agent'，得到 %d 个匹配", len(matches))
		}

		text2 := []byte("this is config")
		matches = m.MatchAll(text2)
		if len(matches) != 1 {
			t.Error("重载后应匹配 'config'")
		}
	})

	// 测试 Sources()
	t.Run("Sources", func(t *testing.T) {
		sources := []string{"agent,api,config"}
		m, err := NewScannerMatcher("src_test", sources)
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}
		got := m.Sources()
		if len(got) != len(sources) {
			t.Errorf("Sources() 长度 = %d, 期望 %d", len(got), len(sources))
		}
	})
}

// ============================================================================
// 5. TestScannerMatcherUnicode — Unicode 支持
// ============================================================================

func TestScannerMatcherUnicode(t *testing.T) {
	// 测试中文字符串中的关键词匹配
	t.Run("ChineseKeywords", func(t *testing.T) {
		m, err := NewScannerMatcher("chinese", []string{"代理,配置,服务器"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		// 使用明确不包含"服务"的文本
		text := []byte("这是一个代理和配置的工具")
		matches := m.MatchAll(text)
		if len(matches) != 2 {
			t.Errorf("匹配数 = %d, 期望 2 (代理和配置)", len(matches))
		}

		keywords := make(map[string]bool)
		for _, m := range matches {
			keywords[m.Keyword] = true
		}

		if !keywords["代理"] {
			t.Error("应匹配到 '代理'")
		}
		if !keywords["配置"] {
			t.Error("应匹配到 '配置'")
		}
	})

	// 测试关键词本身是中文字符
	t.Run("ChineseInDict", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "chinese.txt")
		content := "用户\n接口\n网络\n数据库"
		os.WriteFile(filePath, []byte(content), 0644)

		m, err := NewScannerMatcher("chinese_dict", []string{filePath})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("用户接口通过网络访问数据库")
		matches := m.MatchAll(text)
		if len(matches) != 4 {
			t.Errorf("匹配数 = %d, 期望 4 (用户、接口、网络、数据库)", len(matches))
		}
	})

	// 测试 emoji 等特殊字符
	t.Run("Emoji", func(t *testing.T) {
		m, err := NewScannerMatcher("emoji", []string{"🚀,test"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("发布 🚀 版本")
		matches := m.MatchAll(text)
		if len(matches) != 1 {
			t.Errorf("匹配数 = %d, 期望 1", len(matches))
		}
		if len(matches) > 0 && matches[0].Keyword != "🚀" {
			t.Errorf("Keyword = %q, 期望 '🚀'", matches[0].Keyword)
		}
	})

	// 测试混合 Unicode
	t.Run("MixedUnicode", func(t *testing.T) {
		m, err := NewScannerMatcher("mixed", []string{"agent,代理,API,接口"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("使用 agent 和 API 进行代理接口开发")
		matches := m.MatchAll(text)
		if len(matches) != 4 {
			t.Errorf("匹配数 = %d, 期望 4", len(matches))
		}
	})

	// 测试 CompletionDict 中的中文匹配
	t.Run("CompletionDictChinese", func(t *testing.T) {
		data := []byte("代理\n配置\n服务器\n数据库\n用户")
		d := NewCompletionDict("chinese_completion", nil)
		d.LoadFromData(data)

		results := d.Complete("代")
		if len(results) == 0 {
			t.Error("应匹配到中文前缀 '代'")
		}
		if len(results) > 0 && results[0] != "代理" {
			t.Errorf("第一个结果 = %q, 期望 '代理'", results[0])
		}
	})
}

// ============================================================================
// 6. TestManagerBasic — 管理器基本功能
// ============================================================================

func TestManagerBasic(t *testing.T) {
	tmpDir := t.TempDir()
	keywordFile := filepath.Join(tmpDir, "keywords.txt")
	os.WriteFile(keywordFile, []byte("agent\napi\nconfig\nhttp\nserver"), 0644)

	// 测试 NewManager 创建
	t.Run("Create", func(t *testing.T) {
		m, err := NewManager(nil, keywordFile)
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		if m == nil {
			t.Fatal("NewManager 返回 nil")
		}
		defer m.Close()
	})

	// 测试默认配置（向后兼容，无用户配置时自动创建 autocomplete 词典）
	t.Run("DefaultConfig", func(t *testing.T) {
		m, err := NewManager(nil, "")
		if err != nil {
			t.Fatalf("NewManager(nil, \"\") 失败: %v", err)
		}
		defer m.Close()

		// 应自动创建 autocomplete 词典
		dicts := m.ListDicts()
		if len(dicts) == 0 {
			t.Error("默认配置应创建至少一个词典")
		}

		found := false
		for _, d := range dicts {
			if d == "autocomplete" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("应创建 'autocomplete' 词典，得到: %v", dicts)
		}
	})

	// 测试 AutoComplete 委托
	t.Run("AutoComplete", func(t *testing.T) {
		m, err := NewManager(nil, keywordFile)
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		results := m.AutoComplete("age")
		if len(results) == 0 {
			t.Error("AutoComplete('age') 应返回结果")
		}

		found := false
		for _, r := range results {
			if r == "agent" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AutoComplete 应包含 'agent'，得到: %v", results)
		}
	})

	// 测试 MatchAll 委托到扫描词典
	t.Run("MatchAll", func(t *testing.T) {
		// 创建带扫描词典的配置
		scannerFile := filepath.Join(tmpDir, "scanner.txt")
		os.WriteFile(scannerFile, []byte("agent\napi\nconfig"), 0644)

		config := &KeywordsConfig{
			Scanners: map[string]*ScannerConfig{
				"myscanner": {
					Sources: []string{scannerFile},
				},
			},
		}

		m, err := NewManager(config, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		text := []byte("this contains agent and api")
		matches, err := m.MatchAll("myscanner", text)
		if err != nil {
			t.Fatalf("MatchAll 失败: %v", err)
		}
		if len(matches) != 2 {
			t.Errorf("匹配数 = %d, 期望 2", len(matches))
		}
	})

	// 测试 MatchAll 未找到词典
	t.Run("MatchAllNotFound", func(t *testing.T) {
		config := &KeywordsConfig{
			Scanners: map[string]*ScannerConfig{
				"test": {
					Sources: []string{"agent,api,config"},
				},
			},
		}
		m, err := NewManager(config, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		_, err = m.MatchAll("nonexistent", []byte("test"))
		if err == nil {
			t.Error("MatchAll 对不存在的词典应返回错误")
		}
	})

	// 测试 ListDicts
	t.Run("ListDicts", func(t *testing.T) {
		config := &KeywordsConfig{
			AutoComplete: &AutoCompleteConfig{
				Sources: []string{keywordFile},
			},
			Scanners: map[string]*ScannerConfig{
				"scanner1": {
					Sources: []string{scannerFileForList(t, tmpDir)},
				},
				"scanner2": {
					Sources: []string{scannerFileForList(t, tmpDir)},
				},
			},
		}

		m, err := NewManager(config, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		dicts := m.ListDicts()
		if len(dicts) != 3 { // autocomplete + scanner1 + scanner2
			t.Errorf("ListDicts() 返回 %d 个词典，期望 3", len(dicts))
		}

		dictSet := make(map[string]bool)
		for _, d := range dicts {
			dictSet[d] = true
		}

		if !dictSet["autocomplete"] {
			t.Error("应包含 'autocomplete'")
		}
		if !dictSet["scanner1"] {
			t.Error("应包含 'scanner1'")
		}
		if !dictSet["scanner2"] {
			t.Error("应包含 'scanner2'")
		}
	})

	// 测试 Close
	t.Run("Close", func(t *testing.T) {
		config := &KeywordsConfig{
			AutoComplete: &AutoCompleteConfig{
				Sources: []string{keywordFile},
			},
		}
		m, err := NewManager(config, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}

		err = m.Close()
		if err != nil {
			t.Fatalf("Close 失败: %v", err)
		}

		// 关闭后 AutoComplete 应返回 nil
		results := m.AutoComplete("test")
		if results != nil {
			t.Error("Close 后 AutoComplete 应返回 nil")
		}
	})
}

// 辅助函数：为 ListDicts 测试创建扫描文件
func scannerFileForList(t *testing.T, tmpDir string) string {
	t.Helper()
	filePath := filepath.Join(tmpDir, "list_scanner.txt")
	if err := os.WriteFile(filePath, []byte("agent\napi"), 0644); err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	return filePath
}

// ============================================================================
// 7. TestManagerBackwardCompat — 向后兼容
// ============================================================================

func TestManagerBackwardCompat(t *testing.T) {
	// 测试不提供 [keywords] 配置时的默认行为
	t.Run("NoConfig", func(t *testing.T) {
		m, err := NewManager(nil, "")
		if err != nil {
			t.Fatalf("NewManager(nil, \"\") 失败: %v", err)
		}
		defer m.Close()

		// 应自动创建 autocomplete 词典
		dicts := m.ListDicts()
		if len(dicts) == 0 {
			t.Fatal("默认配置应创建至少一个词典")
		}

		found := false
		for _, d := range dicts {
			if d == "autocomplete" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("应创建 'autocomplete' 词典，得到: %v", dicts)
		}
	})

	// 验证默认配置创建了 autocomplete 词典
	t.Run("AutocompleteCreated", func(t *testing.T) {
		m, err := NewManager(nil, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		// 获取补全词典
		comp := m.GetCompletionDict()
		if comp == nil {
			t.Fatal("应创建补全词典")
		}
		if comp.Name() != "autocomplete" {
			t.Errorf("词典名称 = %q, 期望 'autocomplete'", comp.Name())
		}

		// 应有内置关键词
		if comp.WordCount() < 20 {
			t.Errorf("WordCount() = %d, 期望至少 20", comp.WordCount())
		}
	})

	// 验证 autocomplete 词典可以通过 AutoComplete 方法调用
	t.Run("AutoCompleteCallable", func(t *testing.T) {
		m, err := NewManager(nil, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		// 测试常见词汇
		testCases := []struct {
			prefix   string
			expected string
		}{
			{"pyt", "python"},
			{"jav", "javascript"},
			{"dat", "database"},
			{"mon", "mongodb"},
			{"mid", "middleware"},
		}

		for _, tc := range testCases {
			results := m.AutoComplete(tc.prefix)
			if len(results) == 0 {
				t.Errorf("AutoComplete(%q) 应返回结果", tc.prefix)
				continue
			}

			found := false
			for _, r := range results {
				if r == tc.expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("AutoComplete(%q) 应包含 %q，得到: %v", tc.prefix, tc.expected, results)
			}
		}
	})

	// 测试只传递 defaultPath 的情况
	t.Run("OnlyDefaultPath", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "custom.txt")
		os.WriteFile(filePath, []byte("custom1\ncustom2"), 0644)

		m, err := NewManager(nil, filePath)
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		results := m.AutoComplete("cust")
		if len(results) == 0 {
			t.Error("AutoComplete 应匹配到自定义关键词")
		}
	})
}

// ============================================================================
// 8. TestManagerReload — 热重载
// ============================================================================

func TestManagerReload(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "reloadable.txt")

	// 初始内容
	os.WriteFile(filePath, []byte("agent\napi\nconfig"), 0644)

	// 创建管理器（使用 defaultPath 方式以便 Reload 能正确工作）
	m, err := NewManager(nil, filePath)
	if err != nil {
		t.Fatalf("NewManager 失败: %v", err)
	}
	defer m.Close()

	// 初始加载后验证匹配结果
	t.Run("InitialLoad", func(t *testing.T) {
		results := m.AutoComplete("age")
		found := false
		for _, r := range results {
			if r == "agent" {
				found = true
				break
			}
		}
		if !found {
			t.Error("初始加载应匹配 'agent'")
		}
	})

	// 修改文件内容
	t.Run("ModifyFile", func(t *testing.T) {
		os.WriteFile(filePath, []byte("server\nhttp\nhttps"), 0644)
	})

	// 验证新匹配结果（通过手动重载词典）
	t.Run("AfterReload", func(t *testing.T) {
		// 获取补全词典并手动重载
		comp := m.GetCompletionDict()
		if comp != nil {
			if err := comp.Reload(); err != nil {
				t.Logf("Reload 失败: %v", err)
			}
		}

		// 'agent' 不再存在
		results := m.AutoComplete("age")
		if len(results) > 0 {
			for _, r := range results {
				if r == "agent" {
					t.Error("重载后 'agent' 不应再存在")
				}
			}
		}

		// 'server' 应存在
		results = m.AutoComplete("serv")
		found := false
		for _, r := range results {
			if r == "server" {
				found = true
				break
			}
		}
		if !found {
			t.Error("重载后应匹配到 'server'")
		}
	})

	// 测试扫描器的 Reload
	t.Run("ScannerReload", func(t *testing.T) {
		scannerFile := filepath.Join(tmpDir, "scanner_reload.txt")
		os.WriteFile(scannerFile, []byte("old_keyword"), 0644)

		config := &KeywordsConfig{
			Scanners: map[string]*ScannerConfig{
				"reload_test": {
					Sources: []string{scannerFile},
				},
			},
		}

		m, err := NewManager(config, "")
		if err != nil {
			t.Fatalf("NewManager 失败: %v", err)
		}
		defer m.Close()

		// 初始匹配
		matches, err := m.MatchAll("reload_test", []byte("old_keyword found"))
		if err != nil {
			t.Fatalf("MatchAll 失败: %v", err)
		}
		if len(matches) != 1 {
			t.Error("初始应匹配 'old_keyword'")
		}

		// 修改文件
		os.WriteFile(scannerFile, []byte("new_keyword"), 0644)

		// 重载
		if err := m.ReloadAll(); err != nil {
			t.Logf("ReloadAll 返回错误 (可能): %v", err)
		}

		// 验证新内容
		matches, err = m.MatchAll("reload_test", []byte("new_keyword found"))
		if err != nil {
			t.Fatalf("MatchAll 失败: %v", err)
		}
		if len(matches) != 1 {
			t.Error("重载后应匹配 'new_keyword'")
		}
	})
}

// ============================================================================
// 9. TestCompletionDictConcurrency — 并发安全
// ============================================================================

func TestCompletionDictConcurrency(t *testing.T) {
	data := []byte("agent\napi\nconfig\nhttp\nserver\ndatabase\ncache\nproxy\nrouter\nhandler\n" +
		"middleware\ncontroller\nservice\nclient\nserver2\napi2\nconfig2\nhttp2\nrpc2")
	d := NewCompletionDict("concurrent_test", nil)
	d.LoadFromData(data)

	// 并发调用 Complete()
	t.Run("ConcurrentComplete", func(t *testing.T) {
		const goroutines = 100
		const iterations = 100

		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(iterations int) {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					results := d.Complete("age")
					// 验证结果的一致性
					if results != nil {
						for _, r := range results {
							if !strings.HasPrefix(strings.ToLower(r), "age") {
								t.Errorf("结果 %q 不以前缀 'age' 开头", r)
								return
							}
						}
					}
				}
			}(iterations)
		}
		wg.Wait()
	})

	// 并发调用 Reload() 和 Complete()
	t.Run("ConcurrentReloadAndComplete", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "concurrent.txt")
		os.WriteFile(filePath, []byte("agent\napi\nconfig"), 0644)

		d := NewCompletionDict("reload_concurrent", []string{filePath})

		const goroutines = 50
		const iterations = 50

		var wg sync.WaitGroup

		// 并发 Complete
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					_ = d.Complete("a")
				}
			}()
		}

		// 并发 Reload
		for i := 0; i < goroutines/2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations/2; j++ {
					_ = d.Reload()
				}
			}()
		}

		// 并发 AddWords
		for i := 0; i < goroutines/2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				for j := 0; j < iterations/2; j++ {
					d.AddWords([]string{fmt.Sprintf("word_%d_%d", idx, j)})
				}
			}(i)
		}

		wg.Wait()

		// 验证词典仍然可用
		if d.Complete("") == nil {
			t.Error("并发操作后 Complete() 不应返回 nil")
		}
	})
}

// 需要添加 fmt 包导入，稍后检查

// ============================================================================
// 10. TestScannerMatcherConcurrency — AC 扫描器并发安全
// ============================================================================

func TestScannerMatcherConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "concurrent_scanner.txt")
	os.WriteFile(filePath, []byte("agent\napi\nconfig\nhttp\nserver\ndatabase\ncache\nproxy\nrouter\nhandler\n" +
		"middleware\ncontroller\nservice\nclient\ntest1\ntest2\ntest3\ntest4\ntest5\ntest6"), 0644)

	// 创建扫描器
	m, err := NewScannerMatcher("concurrent_scanner", []string{filePath})
	if err != nil {
		t.Fatalf("NewScannerMatcher 失败: %v", err)
	}

	// 并发调用 MatchAll()
	t.Run("ConcurrentMatchAll", func(t *testing.T) {
		const goroutines = 100
		const iterations = 100

		text := []byte("this text contains agent and api and config and http and server")

		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					matches := m.MatchAll(text)
					if matches != nil {
						// 验证 Match 结构
						for _, match := range matches {
							if match.Keyword == "" {
								t.Error("Keyword 不应为空")
							}
							if match.Start < 0 {
								t.Errorf("Start = %d, 应 >= 0", match.Start)
							}
							if match.End <= match.Start {
								t.Errorf("End(%d) <= Start(%d)", match.End, match.Start)
							}
						}
					}
				}
			}()
		}
		wg.Wait()
	})

	// 并发调用 Reload() 和 MatchAll()
	t.Run("ConcurrentReloadAndMatchAll", func(t *testing.T) {
		const goroutines = 50
		const iterations = 50

		text := []byte("agent api config http server")

		var wg sync.WaitGroup

		// 并发 MatchAll
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					_ = m.MatchAll(text)
				}
			}()
		}

		// 并发 Reload
		for i := 0; i < goroutines/2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations/2; j++ {
					_ = m.Reload()
				}
			}()
		}

		wg.Wait()

		// 验证扫描器仍然可用
		matches := m.MatchAll(text)
		if matches == nil {
			t.Error("并发操作后 MatchAll() 不应返回 nil")
		}
	})
}

// ============================================================================
// 11. 边缘情况测试
// ============================================================================

func TestEdgeCases(t *testing.T) {
	// 空关键词列表
	t.Run("EmptyKeywords", func(t *testing.T) {
		d := NewCompletionDict("empty", nil)
		if d.WordCount() != 0 {
			t.Errorf("WordCount() = %d, 期望 0", d.WordCount())
		}

		results := d.Complete("anything")
		if len(results) != 0 {
			t.Errorf("Complete() 返回 %d 个结果，期望 0", len(results))
		}

		// ScannerMatcher 空关键词
		_, err := NewScannerMatcher("empty_scanner", []string{})
		if err == nil {
			t.Error("空关键词源应返回错误")
		}
	})

	// 空文本匹配
	t.Run("EmptyText", func(t *testing.T) {
		m, err := NewScannerMatcher("empty_text", []string{"agent,api"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		matches := m.MatchAll([]byte(""))
		if len(matches) != 0 {
			t.Errorf("空文本匹配数 = %d, 期望 0", len(matches))
		}
	})

	// 关键词包含特殊字符（如 .、*、-）— AC 应字面匹配
	t.Run("SpecialChars", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "special_kw.txt")
		content := "hello.world\nfoo*bar\nmy-key\ntest_123\na-b-c.d_e"
		os.WriteFile(filePath, []byte(content), 0644)

		m, err := NewScannerMatcher("special", []string{filePath})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		testCases := []struct {
			text    string
			wantKw  string
		}{
			{"hello.world is here", "hello.world"},
			{"using foo*bar pattern", "foo*bar"},
			{"my-key-value", "my-key"},
			{"test_123 found", "test_123"},
		}

		for _, tc := range testCases {
			matches := m.MatchAll([]byte(tc.text))
			found := false
			for _, match := range matches {
				if match.Keyword == tc.wantKw {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("文本 %q 应匹配到 %q", tc.text, tc.wantKw)
			}
		}
	})

	// 非常长的文本（1MB）
	t.Run("LargeText", func(t *testing.T) {
		m, err := NewScannerMatcher("large_text", []string{"agent,api,config"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		// 生成 1MB 文本
		largeText := make([]byte, 1*1024*1024)
		for i := 0; i < len(largeText); i++ {
			largeText[i] = 'a'
		}
		// 插入关键词
		copy(largeText[100:], "agent")
		copy(largeText[10000:], "api")
		copy(largeText[500000:], "config")

		matches := m.MatchAll(largeText)
		if len(matches) != 3 {
			t.Errorf("1MB 文本匹配数 = %d, 期望 3", len(matches))
		}

		// 验证位置
		for _, match := range matches {
			switch match.Keyword {
			case "agent":
				if match.Start != 100 {
					t.Errorf("'agent' Start = %d, 期望 100", match.Start)
				}
			case "api":
				if match.Start != 10000 {
					t.Errorf("'api' Start = %d, 期望 10000", match.Start)
				}
			case "config":
				if match.Start != 500000 {
					t.Errorf("'config' Start = %d, 期望 500000", match.Start)
				}
			}
		}
	})

	// 重复关键词（去重验证）
	t.Run("DuplicateKeywords", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "dupes.txt")
		// 包含重复关键词
		content := "agent\nagent\napi\napi\napi\nconfig\nagent"
		os.WriteFile(filePath, []byte(content), 0644)

		// CompletionDict 测试
		d := NewCompletionDict("dupes_cd", nil)
		d.LoadFromFile(filePath)

		if d.WordCount() != 3 {
			t.Errorf("CompletionDict WordCount() = %d, 期望 3 (去重后)", d.WordCount())
		}

		// ScannerMatcher 测试
		m, err := NewScannerMatcher("dupes_sm", []string{filePath})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		// 验证匹配结果中的关键词不重复
		matches := m.MatchAll([]byte("agent api config"))
		seen := make(map[string]bool)
		for _, match := range matches {
			if seen[match.Keyword] {
				t.Errorf("重复匹配到关键词 %q", match.Keyword)
			}
			seen[match.Keyword] = true
		}
	})

	// 超长关键词
	t.Run("VeryLongKeyword", func(t *testing.T) {
		longWord := strings.Repeat("a", 10000)
		m, err := NewScannerMatcher("long_kw", []string{longWord + ",hello"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("prefix " + longWord + " suffix")
		matches := m.MatchAll(text)
		if len(matches) != 1 {
			t.Errorf("匹配数 = %d, 期望 1", len(matches))
		}
		if len(matches) > 0 && matches[0].Keyword != longWord {
			t.Error("关键词内容不匹配")
		}
	})

	// 关键词包含空格和特殊空白字符
	t.Run("WhitespaceInKeywords", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "whitespace.txt")
		// 每行前后可能有空格
		content := "  agent  \n  api  \n\tconfig\t\n\n\thttp\t"
		os.WriteFile(filePath, []byte(content), 0644)

		d := NewCompletionDict("ws_test", nil)
		d.LoadFromFile(filePath)

		// 关键词应被 trim，不应包含前后空格
		results := d.Complete("agent")
		if len(results) > 0 {
			if results[0] != "agent" {
				t.Errorf("结果 = %q, 期望 'agent' (无前后空格)", results[0])
			}
		}
	})

	// 单字符关键词
	t.Run("SingleCharKeywords", func(t *testing.T) {
		m, err := NewScannerMatcher("single_char", []string{"a,b,c"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		text := []byte("abc")
		matches := m.MatchAll(text)
		if len(matches) != 3 {
			t.Errorf("匹配数 = %d, 期望 3", len(matches))
		}
	})

	// 全大写/全小写/混合大小写关键词
	t.Run("MixedCaseKeywords", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "case.txt")
		content := "agent\napi\nconfig\nhttp\nserver"
		os.WriteFile(filePath, []byte(content), 0644)

		// CompletionDict 大小写不敏感
		d := NewCompletionDict("case_cd", nil)
		d.LoadFromFile(filePath)

		// 验证关键词已加载
		allResults := d.Complete("")
		if len(allResults) != 5 {
			t.Errorf("加载了 %d 个词，期望 5", len(allResults))
		}

		// 测试各种大小写的前缀匹配
		testPrefixes := []string{"age", "AGE", "Age", "api", "API", "conf", "htt", "ser"}
		for _, prefix := range testPrefixes {
			results := d.Complete(prefix)
			if len(results) == 0 {
				t.Errorf("Complete(%q) 应返回结果", prefix)
			}
		}

		// ScannerMatcher 大小写敏感
		m, err := NewScannerMatcher("case_sm", []string{"agent,api,config,http,server"})
		if err != nil {
			t.Fatalf("NewScannerMatcher 失败: %v", err)
		}

		// 测试大小写敏感匹配
		matches := m.MatchAll([]byte("using agent here"))
		if len(matches) != 1 {
			t.Errorf("匹配数 = %d, 期望 1", len(matches))
		}
		if len(matches) > 0 && matches[0].Keyword != "agent" {
			t.Errorf("Keyword = %q, 期望 'agent'", matches[0].Keyword)
		}
	})

	// 关键词是另一个词的前缀
	t.Run("KeywordIsPrefix", func(t *testing.T) {
		data := []byte("test\ntest1\ntest12\ntest123")
		d := NewCompletionDict("prefix_chain", nil)
		d.LoadFromData(data)

		results := d.Complete("test")
		if len(results) != 4 {
			t.Errorf("Complete('test') = %d, 期望 4", len(results))
		}

		// 验证所有结果
		expected := map[string]bool{
			"test":    true,
			"test1":   true,
			"test12":  true,
			"test123": true,
		}
		for _, r := range results {
			if !expected[r] {
				t.Errorf("意外结果: %q", r)
			}
		}
	})
}
