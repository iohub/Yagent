package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
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

// doCodebaseRequest sends an HTTP POST to the codebase service with retry logic.
// Returns the raw response body on success.
func (t *RepoOperationsTool) doCodebaseRequest(endpoint string, body interface{}) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s%s", t.CodebaseURL, endpoint)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		return respBody, nil
	}

	return nil, fmt.Errorf("codebase request failed after 3 retries: %w", lastErr)
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

	body, err := t.doCodebaseRequest("/semantic_search", map[string]interface{}{
		"repo_path": t.ProjectPath,
		"limit":     limit,
		"text":      query,
	})
	if err != nil {
		return nil, err
	}

	var response interface{}
	if err := json.Unmarshal(body, &response); err != nil {
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

	body, err := t.doCodebaseRequest("/query_code_skeleton", map[string]interface{}{
		"filepaths": filepaths,
	})
	if err != nil {
		return nil, err
	}

	var response QueryCodeSkeletonResponse
	if err := json.Unmarshal(body, &response); err != nil {
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

	body, err := t.doCodebaseRequest("/query_code_snippet", map[string]interface{}{
		"filepath":      filepath,
		"function_name": functionName,
	})
	if err != nil {
		return nil, err
	}

	var response QueryCodeSnippetResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return string(body), nil
	}
	if !response.Success {
		return nil, fmt.Errorf("server returned unsuccessful response: %s", string(body))
	}
	return response, nil
}
