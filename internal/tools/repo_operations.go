package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type RepoOperationsTool struct {
	CodebaseURL string
	ProjectPath string
}

func NewRepoOperationsTool(codebaseURL, projectPath string) *RepoOperationsTool {
	return &RepoOperationsTool{
		CodebaseURL: codebaseURL,
		ProjectPath: projectPath,
	}
}

type QueryCodeSkeletonResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Skeletons []struct {
			Filepath     string `json:"filepath"`
			Language     string `json:"language"`
			SkeletonText string `json:"skeleton_text"`
		} `json:"skeletons"`
	} `json:"data"`
}

type QueryCodeSnippetResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Filepath     string `json:"filepath"`
		FunctionName string `json:"function_name"`
		CodeSnippet  string `json:"code_snippet"`
		LineStart    int    `json:"line_start"`
		LineEnd      int    `json:"line_end"`
		Language     string `json:"language"`
	} `json:"data"`
}

func (t *RepoOperationsTool) ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	query, ok := params["query"].(string)
	if !ok {
		return nil, fmt.Errorf("query parameter must be a string")
	}
	limit := 5
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
	}
	if limit <= 0 {
		limit = 5
	}

	requestData := map[string]interface{}{
		"repo_path": t.ProjectPath,
		"limit":     limit,
		"text":      query,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("%s/semantic_search", t.CodebaseURL),
		strings.NewReader(string(jsonData)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
	}

	var response interface{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		// If response is not JSON, return as string
		return string(body), nil
	}

	return response, nil
}

func (t *RepoOperationsTool) ExecuteQueryCodeSkeleton(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filepathsInterface, ok := params["filepaths"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("filepaths parameter must be an array")
	}
	filepaths := make([]string, len(filepathsInterface))
	for i, v := range filepathsInterface {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("filepaths element must be a string")
		}
		filepaths[i] = s
	}

	requestData := map[string]interface{}{
		"filepaths": filepaths,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("%s/query_code_skeleton", t.CodebaseURL),
		strings.NewReader(string(jsonData)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
	}

	var response QueryCodeSkeletonResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		// If response is not JSON, return as string
		return string(body), nil
	}

	if !response.Success {
		return nil, fmt.Errorf("server returned unsuccessful response: %s", string(body))
	}

	return response, nil
}

func (t *RepoOperationsTool) ExecuteQueryCodeSnippet(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filepath, ok := params["filepath"].(string)
	if !ok {
		return nil, fmt.Errorf("filepath parameter must be a string")
	}
	functionName, ok := params["function_name"].(string)
	if !ok {
		return nil, fmt.Errorf("function_name parameter must be a string")
	}

	requestData := map[string]interface{}{
		"filepath":      filepath,
		"function_name": functionName,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("%s/query_code_snippet", t.CodebaseURL),
		strings.NewReader(string(jsonData)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(body))
	}

	var response QueryCodeSnippetResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		// If response is not JSON, return as string
		return string(body), nil
	}

	if !response.Success {
		return nil, fmt.Errorf("server returned unsuccessful response: %s", string(body))
	}

	return response, nil
}
