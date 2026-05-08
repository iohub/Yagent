package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkills_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	registry, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registry.Count() != 0 {
		t.Errorf("expected 0 skills, got %d", registry.Count())
	}
}

func TestLoadSkills_NonExistentDirectory(t *testing.T) {
	registry, err := LoadSkills("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registry.Count() != 0 {
		t.Errorf("expected 0 skills, got %d", registry.Count())
	}
}

func TestLoadSkills_WithMarkdownFiles(t *testing.T) {
	dir := t.TempDir()

	// 创建测试文件
	testContent := "# Test Skill\nThis is a test skill."
	err := os.WriteFile(filepath.Join(dir, "test-skill.md"), []byte(testContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	registry, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if registry.Count() != 1 {
		t.Fatalf("expected 1 skill, got %d", registry.Count())
	}

	skill, ok := registry.Get("test-skill")
	if !ok {
		t.Fatal("skill 'test-skill' not found")
	}

	if skill.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got '%s'", skill.Name)
	}

	if skill.Description != "Test Skill" {
		t.Errorf("expected description 'Test Skill', got '%s'", skill.Description)
	}

	if skill.Content != testContent {
		t.Errorf("content mismatch")
	}
}

func TestLoadSkills_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"skill-a.md": "# Skill A\nContent A",
		"skill-b.md": "# Skill B\nContent B",
		"readme.txt": "This should be ignored",
	}

	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	registry, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}

	if registry.Count() != 2 {
		t.Errorf("expected 2 skills, got %d", registry.Count())
	}

	if _, ok := registry.Get("skill-a"); !ok {
		t.Error("skill-a not found")
	}

	if _, ok := registry.Get("skill-b"); !ok {
		t.Error("skill-b not found")
	}

	if _, ok := registry.Get("readme"); ok {
		t.Error("readme.txt should not be loaded")
	}
}

func TestSkillRegistry_Match(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "my-skill.md"), []byte("# My Skill\nContent"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	registry, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 测试带 : 前缀
	skill, ok := registry.Match(":my-skill")
	if !ok {
		t.Fatal("expected to find skill with :my-skill")
	}
	if skill.Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got '%s'", skill.Name)
	}

	// 测试不带 : 前缀
	skill, ok = registry.Match("my-skill")
	if !ok {
		t.Fatal("expected to find skill with my-skill")
	}

	// 测试不存在
	_, ok = registry.Match(":nonexistent")
	if ok {
		t.Error("expected not to find nonexistent skill")
	}
}

func TestSkillRegistry_List(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"alpha.md":  "# Alpha",
		"beta.md":   "# Beta",
		"gamma.md":  "# Gamma",
	}
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	registry, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}

	list := registry.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 skills in list, got %d", len(list))
	}

	// 验证列表包含所有 skill（注意：skill 名不包含 .md 后缀）
	expectedSkills := []string{"alpha", "beta", "gamma"}
	for _, expected := range expectedSkills {
		found := false
		for _, name := range list {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected '%s' in list", expected)
		}
	}
}
