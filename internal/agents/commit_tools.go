package agents

import (
	"context"
	"fmt"
)

// ==================== 工具定义 ====================

// LearnCommitsToolDef learn_commits 工具定义
//
// 用于触发 git commit 学习流程：获取最近 N 个 commit，
// 使用 LLM 生成结构化摘要，并将嵌入存储到向量数据库。
var LearnCommitsToolDef = ToolDefinition{
	Name:        "learn_commits",
	Description: "Learn from recent git commits. Fetches the last N commits, generates structured summaries using LLM, and stores embeddings for similarity search. Returns the number of commits learned.",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"max_commits": map[string]interface{}{
				"type":        "integer",
				"description": "Number of recent commits to learn (default: config value, usually 30)",
			},
			"repo_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the git repository (default: current working directory)",
			},
		},
	},
}

// SearchSimilarCommitsToolDef search_similar_commits 工具定义
//
// 用于搜索与给定查询相似的 git commit。使用向量嵌入查找语义相似的 commit，
// 返回匹配摘要和相似度分数。
var SearchSimilarCommitsToolDef = ToolDefinition{
	Name:        "search_similar_commits",
	Description: "Search for git commits similar to the given query. Uses vector embeddings to find semantically similar commits. Returns matching commit summaries with similarity scores.",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The query to search for similar commits",
			},
			"top_k": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return (default: 3)",
			},
		},
		"required": []interface{}{"query"},
	},
}

// ==================== 工具执行器 ====================

// LearnCommitsToolExec 执行 learn_commits 工具
//
// 获取最近 N 个 commit，使用 LLM 生成结构化摘要，
// 并将嵌入存储到向量数据库。
//
// 参数:
//   - ctx: 上下文
//   - params: 工具参数，支持 max_commits (int) 和 repo_path (string)
//
// 返回值:
//   - map[string]interface{}: 包含 learned_count (int) 和 commits (string)
//   - error: 执行失败时返回错误
func LearnCommitsToolExec(ctx context.Context, cm *CommitManager, repoPath string) (interface{}, error) {
	if cm == nil {
		return nil, fmt.Errorf("commit manager is not initialized")
	}

	// 获取 CommitLearner 实例
	learner, err := cm.GetLearner()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit learner: %w", err)
	}
	if learner == nil {
		return nil, fmt.Errorf("commit learner is nil")
	}

	// 使用默认配置值
	maxCommits := learner.Config().MaxCommits
	if maxCommits <= 0 {
		maxCommits = DefaultCommitLearnConfig().MaxCommits
	}

	// 确定仓库路径
	path := repoPath
	if path == "" {
		path = "."
	}

	// 获取最近的 commits
	commits, err := learner.FetchRecentCommits(ctx, path, maxCommits)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch commits: %w", err)
	}

	if len(commits) == 0 {
		return map[string]interface{}{
			"learned_count": 0,
			"commits":       "No commits found in the repository.",
		}, nil
	}

	// 使用 LLM 生成摘要
	summaries, err := learner.SummarizeCommits(ctx, commits)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize commits: %w", err)
	}

	// 存储嵌入到向量数据库
	if err := learner.StoreEmbeddings(ctx, summaries); err != nil {
		return nil, fmt.Errorf("failed to store embeddings: %w", err)
	}

	// 格式化为可读文本
	commitsText := FormatSummaryAsText(summaries)

	return map[string]interface{}{
		"learned_count": len(summaries),
		"commits":       commitsText,
	}, nil
}

// SearchSimilarCommitsToolExec 执行 search_similar_commits 工具
//
// 搜索与给定查询相似的 commit，使用向量嵌入进行语义搜索。
//
// 参数:
//   - ctx: 上下文
//   - cm: CommitManager 实例
//   - query: 搜索查询
//   - topK: 返回结果数量
//
// 返回值:
//   - map[string]interface{}: 包含 matches (string) 和 count (int)
//   - error: 执行失败时返回错误
func SearchSimilarCommitsToolExec(ctx context.Context, cm *CommitManager, query string, topK int) (interface{}, error) {
	if cm == nil {
		return nil, fmt.Errorf("commit manager is not initialized")
	}

	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// 获取 CommitLearner 实例
	learner, err := cm.GetLearner()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit learner: %w", err)
	}
	if learner == nil {
		return nil, fmt.Errorf("commit learner is nil")
	}

	// 使用默认配置值
	if topK <= 0 {
		topK = learner.Config().TopK
	}
	if topK <= 0 {
		topK = DefaultCommitLearnConfig().TopK
	}

	// 搜索相似的 commits
	summaries, err := learner.SearchSimilar(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("failed to search similar commits: %w", err)
	}

	// 格式化为可读文本
	matchesText := FormatSummaryAsText(summaries)
	if matchesText == "" {
		matchesText = "No similar commits found."
	}

	return map[string]interface{}{
		"matches": matchesText,
		"count":   len(summaries),
	}, nil
}
