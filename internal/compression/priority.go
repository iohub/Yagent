package compression

// toolTruncationPriority 返回工具的截断优先级
// 优先级 0: 主要工具，优先被截断
// 优先级 1: 次要工具，较后被截断
// 优先级 -1: 受保护工具，永不截断
func toolTruncationPriority(toolName string) int {
	switch toolName {
	case "create_file", "search_replace_in_file", "read_file", "run_bash",
		"semantic_search", "query_code_skeleton", "query_code_snippet",
		"print_dir_tree", "search_by_regex", "query_call_graph",
		"find_function_callee", "find_function_caller":
		return 0
	case "deepthinking":
		return -1
	default:
		return 1
	}
}