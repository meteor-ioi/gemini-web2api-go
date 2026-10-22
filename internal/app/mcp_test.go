package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMCPInitialize(t *testing.T) {
	req := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"1"`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	}
	resp := dispatchMCP(req, "http://localhost:8083")
	if resp == nil {
		t.Fatal("expected non-nil response for initialize")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	resMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if resMap["protocolVersion"] != "2025-06-18" {
		t.Errorf("expected protocolVersion 2025-06-18, got %v", resMap["protocolVersion"])
	}
	srvInfo, _ := resMap["serverInfo"].(map[string]interface{})
	if srvInfo["name"] != "gemini-mcp-server" {
		t.Errorf("expected server name gemini-mcp-server, got %v", srvInfo["name"])
	}
}

func TestMCPToolsList(t *testing.T) {
	req := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"2"`),
		Method:  "tools/list",
	}
	resp := dispatchMCP(req, "http://localhost:8083")
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %v", resp)
	}
	resMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	tools, ok := resMap["tools"].([]interface{})
	if !ok {
		t.Fatalf("unexpected tools type: %T", resMap["tools"])
	}

	expectedTools := map[string]bool{
		"web_search":        false,
		"generate_image":    false,
		"generate_music":    false,
		"create_music_task": false,
		"get_music_status":  false,
		"create_video_task": false,
		"get_video_status":  false,
	}

	for _, it := range tools {
		tm, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		if name == "gemini_chat" {
			t.Errorf("gemini_chat should have been removed from MCP tools")
		}
		if _, exists := expectedTools[name]; exists {
			expectedTools[name] = true
		}
		if tm["description"] == "" {
			t.Errorf("tool %s missing description", name)
		}
		if tm["inputSchema"] == nil {
			t.Errorf("tool %s missing inputSchema", name)
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("expected tool %s not found in tools/list", name)
		}
	}
}

func TestMCPToolsCallUnknown(t *testing.T) {
	req := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"3"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"unknown_tool","arguments":{}}`),
	}
	resp := dispatchMCP(req, "http://localhost:8083")
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
}

func TestMCPToolsCallEmptyParamValidation(t *testing.T) {
	toolsToCheck := []struct {
		name string
		args string
	}{
		{"web_search", `{"query":""}`},
		{"generate_image", `{"prompt":""}`},
		{"generate_music", `{"prompt":""}`},
		{"create_video_task", `{"prompt":""}`},
		{"get_video_status", `{"job_id":""}`},
	}

	for _, tc := range toolsToCheck {
		req := &rpcRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`"test"`),
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"` + tc.name + `","arguments":` + tc.args + `}`),
		}
		resp := dispatchMCP(req, "http://localhost:8083")
		if resp == nil {
			t.Fatalf("expected response for tool %s", tc.name)
		}
		resMap, ok := resp.Result.(map[string]interface{})
		if !ok {
			t.Fatalf("tool %s did not return a map result", tc.name)
		}
		if resMap["isError"] != true {
			t.Errorf("expected isError: true for tool %s with empty argument", tc.name)
		}
	}
}

func TestMCPVideoTaskFlow(t *testing.T) {
	// 1. 测试直接查不存在的任务
	req := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"v1"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"get_video_status","arguments":{"job_id":"video_nonexistent"}}`),
	}
	resp := dispatchMCP(req, "http://localhost:8083")
	if resp == nil {
		t.Fatal("expected response")
	}
	resMap, _ := resp.Result.(map[string]interface{})
	if resMap["isError"] != true {
		t.Errorf("expected error for nonexistent video job")
	}

	// 2. 插入一个假 job 验证查询
	fakeJob := &videoJob{
		ID:        "video_test_123",
		Object:    "video",
		Model:     "gemini-video",
		Status:    "completed",
		CreatedAt: time.Now().Unix(),
		Prompt:    "a flying cat",
		mp4:       []byte("fake_mp4_bytes"),
		mime:      "video/mp4",
	}
	putVideoJob(fakeJob)

	reqStatus := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"v2"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"get_video_status","arguments":{"job_id":"video_test_123"}}`),
	}
	respStatus := dispatchMCP(reqStatus, "http://localhost:8083")
	if respStatus == nil || respStatus.Error != nil {
		t.Fatalf("failed to get video status: %v", respStatus)
	}
	resMapStatus, _ := respStatus.Result.(map[string]interface{})
	if resMapStatus["isError"] == true {
		t.Fatalf("unexpected isError for existing completed job")
	}
	contentList, _ := resMapStatus["content"].([]interface{})
	if len(contentList) == 0 {
		t.Fatal("expected content in result")
	}
	textItem, _ := contentList[0].(map[string]interface{})
	text, _ := textItem["text"].(string)
	if !contains(text, "video_test_123") || !contains(text, "completed") {
		t.Errorf("expected job id and completed in text output, got %s", text)
	}
}

func TestMCPMusicTaskFlow(t *testing.T) {
	// 1. 无 Cookie 时直接失败
	req := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"m1"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"create_music_task","arguments":{"prompt":"lo-fi hiphop beats"}}`),
	}
	resp := dispatchMCP(req, "http://localhost:8083")
	if resp == nil || resp.Error != nil {
		t.Fatalf("unexpected failure: %v", resp)
	}
	resMap, _ := resp.Result.(map[string]interface{})
	if resMap["isError"] != true {
		t.Errorf("expected error when no cookie available")
	}

	// 2. 模拟一个完成的任务，验证 get_music_status
	fakeJob := &musicJob{
		ID:        "music_test_123",
		Object:    "music",
		Model:     "gemini-music",
		Status:    "completed",
		CreatedAt: time.Now().Unix(),
		Prompt:    "lo-fi hiphop beats",
		mp3:       []byte("fake_mp3_content"),
		mime:      "audio/mpeg",
		mediaID:   "music_fake_456.mp3",
	}
	putMusicJob(fakeJob)

	reqStatus := &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"m2"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"get_music_status","arguments":{"job_id":"music_test_123"}}`),
	}
	respStatus := dispatchMCP(reqStatus, "http://localhost:8083")
	if respStatus == nil || respStatus.Error != nil {
		t.Fatalf("failed to get music status: %v", respStatus)
	}
	resMapStatus, _ := respStatus.Result.(map[string]interface{})
	if resMapStatus["isError"] == true {
		t.Fatalf("unexpected isError for existing completed music job")
	}
	contentList, _ := resMapStatus["content"].([]interface{})
	if len(contentList) == 0 {
		t.Fatal("expected content in result")
	}
	textItem, _ := contentList[0].(map[string]interface{})
	text, _ := textItem["text"].(string)
	if !contains(text, "music_test_123") || !contains(text, "completed") || !contains(text, "music_fake_456.mp3") {
		t.Errorf("expected job id, completed, and media url in text output, got %s", text)
	}
}

func TestMCPHTTPHandler(t *testing.T) {
	// 1. GET 405
	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest("GET", "/mcp", nil)
	handleMCPHTTP(recGet, reqGet)
	if recGet.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected GET 405, got %d", recGet.Code)
	}

	// 2. OPTIONS 204
	recOpt := httptest.NewRecorder()
	reqOpt := httptest.NewRequest("OPTIONS", "/mcp", nil)
	handleMCPHTTP(recOpt, reqOpt)
	if recOpt.Code != http.StatusNoContent {
		t.Errorf("expected OPTIONS 204, got %d", recOpt.Code)
	}

	// 3. POST JSON-RPC ping
	payload := []byte(`{"jsonrpc":"2.0","id":"ping-1","method":"ping"}`)
	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest("POST", "/mcp", bytes.NewReader(payload))
	reqPost.Header.Set("Content-Type", "application/json")
	handleMCPHTTP(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Errorf("expected POST 200, got %d", recPost.Code)
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(recPost.Body.Bytes(), &rpcResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if string(rpcResp.ID) != `"ping-1"` {
		t.Errorf("expected id ping-1, got %s", string(rpcResp.ID))
	}
}

func TestMediaCacheGet(t *testing.T) {
	rawBytes := []byte("fake_image_bytes_png")
	mediaID := saveMediaArtifact("testimg", "image/png", rawBytes)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/media/"+mediaID, nil)
	handleMediaGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("expected Content-Type image/png, got %s", rec.Header().Get("Content-Type"))
	}
	if !bytes.Equal(rec.Body.Bytes(), rawBytes) {
		t.Errorf("expected body %v, got %v", rawBytes, rec.Body.Bytes())
	}
}
