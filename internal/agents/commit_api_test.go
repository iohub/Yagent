package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// ============================================================================
// 测试响应结构体
// ============================================================================

// ApiResponse 通用 API 响应
type ApiResponse struct {
	Success bool   `json:"success"`
	Data    any    `json:"data"`
}

// SearchApiResponse 搜索 API 响应
type SearchApiResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Matches []struct {
			CommitHash  string  `json:"commit_hash"`
			SummaryText string  `json:"summary_text"`
			Similarity  float32 `json:"similarity"`
		} `json:"matches"`
	} `json:"data"`
}

// ============================================================================
// 测试请求结构体
// ============================================================================

// CommitEmbedRequest embed 请求
type CommitEmbedRequest struct {
	CommitHash  string `json:"commit_hash"`
	SummaryText string `json:"summary_text"`
}

// CommitSearchRequest search 请求
type CommitSearchRequest struct {
	Query     string  `json:"query"`
	TopK      int     `json:"top_k"`
	Threshold float32 `json:"threshold"`
}

// ============================================================================
// 辅助函数
// ============================================================================

// getBaseURL 获取 API 基础 URL，支持环境变量覆盖端口
func getBaseURL(t *testing.T) string {
	port := os.Getenv("CODEXRAY_PORT")
	if port == "" {
		port = "12800"
	}
	return fmt.Sprintf("http://127.0.0.1:%s", port)
}

// checkServerReachable 检查服务器是否可访问
func checkServerReachable(t *testing.T, baseURL string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Logf("⚠️  Server not reachable at %s/health: %v", baseURL, err)
		return false
	}
	defer resp.Body.Close()
	return true
}

// printTestTitle 打印醒目的测试标题
func printTestTitle(t *testing.T, testName string) {
	t.Logf("\n" + "============================================================")
	t.Logf("🧪 %s", testName)
	t.Logf("============================================================")
}

// printRequestInfo 打印请求信息
func printRequestInfo(method, url string, body any) {
	fmt.Printf("\n%s %s\n", method, url)
	if body != nil {
		jsonBytes, err := json.MarshalIndent(body, "", "  ")
		if err == nil {
			fmt.Printf("📥 Request Body:\n%s\n", string(jsonBytes))
		}
	}
}

// printResponseInfo 打印响应信息
func printResponseInfo(resp *http.Response, body []byte) {
	fmt.Printf("\n📤 Response Status: %d %s\n", resp.StatusCode, resp.Status)
	if len(body) > 0 {
		jsonBytes, err := json.MarshalIndent(body, "", "  ")
		if err == nil {
			fmt.Printf("📤 Response Body:\n%s\n", string(jsonBytes))
		}
	}
}

// doRequest 执行 HTTP 请求并返回响应体和状态码
func doRequest(method, url string, body any) (*http.Response, []byte, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	var reqBody io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBytes)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return resp, bodyBytes, nil
}

// ============================================================================
// 测试函数
// ============================================================================

// TestCommitEmbedAPI 测试 POST /commit/embed 接口
func TestCommitEmbedAPI(t *testing.T) {
	printTestTitle(t, "🔧 Testing POST /commit/embed")

	baseURL := getBaseURL(t)

	// 检查服务器是否可访问
	if !checkServerReachable(t, baseURL) {
		t.Skip("Server is not running, skipping test")
	}

	url := baseURL + "/commit/embed"
	reqBody := CommitEmbedRequest{
		CommitHash:  "abc123def456789",
		SummaryText: "Requirement: Implement user authentication\nFiles: auth/login.go, auth/middleware.go\nApproach: JWT-based authentication\nImplementation: Added JWT middleware and login endpoint",
	}

	printRequestInfo("POST", url, reqBody)

	resp, bodyBytes, err := doRequest("POST", url, reqBody)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	printResponseInfo(resp, bodyBytes)

	// 断言响应状态码
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 断言响应结构
	var apiResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !apiResp.Success {
		t.Error("Expected success: true")
	}

	fmt.Println("\n✅ Test passed!")
	fmt.Println("============================================================")
}

// TestCommitSearchAPI 测试 POST /commit/search 接口
func TestCommitSearchAPI(t *testing.T) {
	printTestTitle(t, "🔍 Testing POST /commit/search")

	baseURL := getBaseURL(t)

	// 检查服务器是否可访问
	if !checkServerReachable(t, baseURL) {
		t.Skip("Server is not running, skipping test")
	}

	url := baseURL + "/commit/search"
	reqBody := CommitSearchRequest{
		Query:     "user authentication login",
		TopK:      5,
		Threshold: 0.0,
	}

	printRequestInfo("POST", url, reqBody)

	resp, bodyBytes, err := doRequest("POST", url, reqBody)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	printResponseInfo(resp, bodyBytes)

	// 断言响应状态码
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 断言响应结构
	var searchResp SearchApiResponse
	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !searchResp.Success {
		t.Error("Expected success: true")
	}

	// 打印匹配结果
	fmt.Println("\n📊 Search Results:")
	for i, match := range searchResp.Data.Matches {
		fmt.Printf("  [%d] Commit: %s\n", i+1, match.CommitHash)
		fmt.Printf("      Similarity: %.4f\n", match.Similarity)
		fmt.Printf("      Summary: %s\n", match.SummaryText)
	}

	fmt.Println("\n✅ Test passed!")
	fmt.Println("============================================================")
}

// TestCommitClearAPI 测试 POST /commit/clear 接口
func TestCommitClearAPI(t *testing.T) {
	printTestTitle(t, "🧹 Testing POST /commit/clear")

	baseURL := getBaseURL(t)

	// 检查服务器是否可访问
	if !checkServerReachable(t, baseURL) {
		t.Skip("Server is not running, skipping test")
	}

	url := baseURL + "/commit/clear"
	reqBody := struct{}{}

	printRequestInfo("POST", url, reqBody)

	resp, bodyBytes, err := doRequest("POST", url, reqBody)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	printResponseInfo(resp, bodyBytes)

	// 断言响应状态码
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 断言响应结构
	var apiResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !apiResp.Success {
		t.Error("Expected success: true")
	}

	fmt.Println("\n✅ Test passed!")
	fmt.Println("============================================================")
}

// TestCommitAPILifecycle 完整流程测试：embed → search → clear → search
func TestCommitAPILifecycle(t *testing.T) {
	printTestTitle(t, "🔄 Testing Complete Commit API Lifecycle")

	baseURL := getBaseURL(t)

	// 检查服务器是否可访问
	if !checkServerReachable(t, baseURL) {
		t.Skip("Server is not running, skipping test")
	}

	// 测试数据
	testCommit := CommitEmbedRequest{
		CommitHash:  "lifecycle_test_abc123",
		SummaryText: "Requirement: Implement user authentication\nFiles: auth/login.go, auth/middleware.go\nApproach: JWT-based authentication\nImplementation: Added JWT middleware and login endpoint",
	}

	// ========== Step 1: Embed ==========
	t.Log("\n📌 Step 1: Embed test commit")
	embedURL := baseURL + "/commit/embed"
	printRequestInfo("POST", embedURL, testCommit)

	resp, bodyBytes, err := doRequest("POST", embedURL, testCommit)
	if err != nil {
		t.Fatalf("Embed request failed: %v", err)
	}
	printResponseInfo(resp, bodyBytes)

	var embedResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &embedResp); err != nil {
		t.Fatalf("Failed to parse embed response: %v", err)
	}
	if !embedResp.Success {
		t.Fatal("Embed failed: success is false")
	}
	t.Log("✅ Embed successful")

	// ========== Step 2: Search (should find the embedded commit) ==========
	t.Log("\n📌 Step 2: Search for the embedded commit")
	searchURL := baseURL + "/commit/search"
	searchReq := CommitSearchRequest{
		Query:     "user authentication login",
		TopK:      5,
		Threshold: 0.0,
	}
	printRequestInfo("POST", searchURL, searchReq)

	resp, bodyBytes, err = doRequest("POST", searchURL, searchReq)
	if err != nil {
		t.Fatalf("Search request failed: %v", err)
	}
	printResponseInfo(resp, bodyBytes)

	var searchResp SearchApiResponse
	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		t.Fatalf("Failed to parse search response: %v", err)
	}
	if !searchResp.Success {
		t.Fatal("Search failed: success is false")
	}

	// 验证能找到刚才存入的数据
	found := false
	for _, match := range searchResp.Data.Matches {
		t.Logf("  Found match: %s (similarity: %.4f)", match.CommitHash, match.Similarity)
		if match.CommitHash == testCommit.CommitHash {
			found = true
		}
	}

	if !found {
		t.Error("❌ Expected to find the embedded commit in search results, but it was not found")
	} else {
		t.Log("✅ Found the embedded commit in search results")
	}

	// ========== Step 3: Clear ==========
	t.Log("\n📌 Step 3: Clear all commit data")
	clearURL := baseURL + "/commit/clear"
	printRequestInfo("POST", clearURL, struct{}{})

	resp, bodyBytes, err = doRequest("POST", clearURL, struct{}{})
	if err != nil {
		t.Fatalf("Clear request failed: %v", err)
	}
	printResponseInfo(resp, bodyBytes)

	var clearResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &clearResp); err != nil {
		t.Fatalf("Failed to parse clear response: %v", err)
	}
	if !clearResp.Success {
		t.Fatal("Clear failed: success is false")
	}
	t.Log("✅ Clear successful")

	// ========== Step 4: Search again (should be empty) ==========
	t.Log("\n📌 Step 4: Search again to verify data is cleared")
	printRequestInfo("POST", searchURL, searchReq)

	resp, bodyBytes, err = doRequest("POST", searchURL, searchReq)
	if err != nil {
		t.Fatalf("Search request failed: %v", err)
	}
	printResponseInfo(resp, bodyBytes)

	var searchResp2 SearchApiResponse
	if err := json.Unmarshal(bodyBytes, &searchResp2); err != nil {
		t.Fatalf("Failed to parse search response: %v", err)
	}
	if !searchResp2.Success {
		t.Fatal("Search failed: success is false")
	}

	if len(searchResp2.Data.Matches) > 0 {
		t.Errorf("❌ Expected no matches after clear, but got %d matches", len(searchResp2.Data.Matches))
		for _, match := range searchResp2.Data.Matches {
			t.Logf("  Unexpected match: %s (similarity: %.4f)", match.CommitHash, match.Similarity)
		}
	} else {
		t.Log("✅ Search returned no matches after clear - data successfully cleared")
	}

	fmt.Println("\n✅ Lifecycle test passed!")
	fmt.Println("============================================================")
}
