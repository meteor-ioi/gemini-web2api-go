package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MCP（Model Context Protocol）服务器：把 Gemini 网页端的各项核心能力暴露为
// MCP Tools，供 Claude Desktop / Claude Code / Cursor / Antigravity 等 MCP 客户端调用。
//
// 暴露的工具列表：
//  1. web_search: Gemini 联网搜索（带 Grounding 引用来源）
//  2. generate_image: 生图工具（Nano Banana，输出标准 MCP image + 短链 URL Markdown）
//  3. generate_music: 原创音乐同步生成（Lyria，约 30 秒，输出短链 audio URL）
//  4. create_music_task: 异步音乐生成任务创建（Google DeepMind Lyria，防超时推荐）
//  5. get_music_status: 查询音乐任务状态及音频直链
//  6. create_video_task: 异步视频生成任务创建（Google Veo）
//  7. get_video_status: 查询视频任务生成状态及下载地址
//
// 传输走 HTTP（Streamable HTTP），挂在后端现有 --port 上的 /mcp，跟 OpenAI 接口
// 同一个进程、同一个端口——部署成服务器后远程客户端连 URL 就能用，复用现成的
// 账号池 / 代理池 / 限流。（没做 stdio 那种客户端拉起本地子进程的传输。）

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleMCPHTTP 是 MCP 的 HTTP 传输（Streamable HTTP），挂在后端 `/mcp` 上，跟
// OpenAI 接口同一个进程、同一个端口，复用现有的账号池 / 代理池 / 限流。
//
// 客户端 POST 一条 JSON-RPC 消息，我们回一条 application/json 响应。工具场景是
// 纯请求-响应，不需要服务端主动推送，所以不开 SSE 流：GET 直接回 405。
// 通知类（无 id）按规范回 202 空体。
func handleMCPHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		// 不提供服务端主动推送的 SSE 流；规范允许这么回。
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP endpoint is POST-only (no server-initiated stream)", http.StatusMethodNotAllowed)
		return
	case http.MethodPost:
		// 落到下面处理
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeMCPHTTPError(w, nil, -32700, "read error")
		return
	}
	var req rpcRequest
	if json.Unmarshal(body, &req) != nil {
		writeMCPHTTPError(w, nil, -32700, "parse error")
		return
	}
	baseURL := getBaseURLFromRequest(r)
	resp := dispatchMCP(&req, baseURL)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted) // 通知：无响应体
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(resp)
	w.Write(b)
}

func writeMCPHTTPError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	w.Write(b)
}

func dispatchMCP(req *rpcRequest, baseURL string) *rpcResponse {
	// 没有 id 的是通知（notifications/initialized 等），不回响应。
	if len(req.ID) == 0 {
		return nil
	}
	if baseURL == "" {
		baseURL = "http://localhost:8083"
	}
	ok := func(result interface{}) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		// 回显客户端请求的协议版本（拿不到就用我们支持的）。
		ver := mcpProtocolVersion
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			ver = p.ProtocolVersion
		}
		return ok(map[string]interface{}{
			"protocolVersion": ver,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "gemini-mcp-server", "version": Version},
		})

	case "tools/list":
		return ok(map[string]interface{}{
			"tools": []interface{}{
				webSearchToolDef(),
				generateImageToolDef(),
				generateMusicToolDef(),
				createMusicTaskToolDef(),
				getMusicStatusToolDef(),
				createVideoTaskToolDef(),
				getVideoStatusToolDef(),
			},
		})

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return fail(-32602, "invalid params")
		}

		switch p.Name {
		case "web_search":
			var args struct {
				Query string `json:"query"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for web_search")
			}
			query := strings.TrimSpace(args.Query)
			if query == "" {
				return ok(toolTextError("query 不能为空"))
			}
			text, err := mcpWebSearch(query)
			if err != nil {
				return ok(toolTextError("搜索失败: " + err.Error()))
			}
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
			})

		case "generate_image":
			var args struct {
				Prompt   string `json:"prompt"`
				ImageURL string `json:"image_url"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for generate_image")
			}
			if strings.TrimSpace(args.Prompt) == "" {
				return ok(toolTextError("prompt 不能为空"))
			}
			contents, err := mcpGenerateImage(args.Prompt, args.ImageURL, baseURL)
			if err != nil {
				return ok(toolTextError("生图失败: " + err.Error()))
			}
			return ok(map[string]interface{}{
				"content": contents,
			})

		case "generate_music":
			var args struct {
				Prompt string `json:"prompt"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for generate_music")
			}
			if strings.TrimSpace(args.Prompt) == "" {
				return ok(toolTextError("prompt 不能为空"))
			}
			text, err := mcpGenerateMusic(args.Prompt, baseURL)
			if err != nil {
				return ok(toolTextError("生成音乐失败: " + err.Error()))
			}
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
			})

		case "create_music_task":
			var args struct {
				Prompt string `json:"prompt"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for create_music_task")
			}
			if strings.TrimSpace(args.Prompt) == "" {
				return ok(toolTextError("prompt 不能为空"))
			}
			res, err := mcpCreateMusicTask(args.Prompt, baseURL)
			if err != nil {
				return ok(toolTextError("创建音乐任务失败: " + err.Error()))
			}
			resJSON, _ := json.MarshalIndent(res, "", "  ")
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": string(resJSON)}},
			})

		case "get_music_status":
			var args struct {
				JobID string `json:"job_id"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for get_music_status")
			}
			if strings.TrimSpace(args.JobID) == "" {
				return ok(toolTextError("job_id 不能为空"))
			}
			res, err := mcpGetMusicStatus(args.JobID, baseURL)
			if err != nil {
				return ok(toolTextError("查询音乐状态失败: " + err.Error()))
			}
			resJSON, _ := json.MarshalIndent(res, "", "  ")
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": string(resJSON)}},
			})

		case "create_video_task":
			var args struct {
				Prompt string `json:"prompt"`
				Model  string `json:"model"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for create_video_task")
			}
			if strings.TrimSpace(args.Prompt) == "" {
				return ok(toolTextError("prompt 不能为空"))
			}
			res, err := mcpCreateVideoTask(args.Prompt, args.Model)
			if err != nil {
				return ok(toolTextError("创建视频任务失败: " + err.Error()))
			}
			resJSON, _ := json.MarshalIndent(res, "", "  ")
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": string(resJSON)}},
			})

		case "get_video_status":
			var args struct {
				JobID string `json:"job_id"`
			}
			if len(p.Arguments) > 0 && json.Unmarshal(p.Arguments, &args) != nil {
				return fail(-32602, "invalid arguments for get_video_status")
			}
			if strings.TrimSpace(args.JobID) == "" {
				return ok(toolTextError("job_id 不能为空"))
			}
			res, err := mcpGetVideoStatus(args.JobID, baseURL)
			if err != nil {
				return ok(toolTextError("查询视频状态失败: " + err.Error()))
			}
			resJSON, _ := json.MarshalIndent(res, "", "  ")
			return ok(map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": string(resJSON)}},
			})

		default:
			return fail(-32602, "unknown tool: "+p.Name)
		}

	case "ping":
		return ok(map[string]interface{}{})

	default:
		return fail(-32601, "method not found: "+req.Method)
	}
}

// webSearchToolDef 是 web_search 工具的定义（含 JSON Schema）。
func webSearchToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "web_search",
		"description": "Search the web via Google Gemini and return a synthesized answer " +
			"with source links. Use for current events, facts, or anything needing " +
			"up-to-date information from the internet.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query or question to research.",
				},
			},
			"required": []interface{}{"query"},
		},
	}
}

// generateImageToolDef 是 generate_image 工具的定义。
func generateImageToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "generate_image",
		"description": "Generate high-quality images using Gemini's image generation engine (Nano Banana). " +
			"Returns clean HTTP image URL. Supports text-to-image and image-to-image (with optional reference image). Requires a valid Google account cookie.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "Detailed image generation prompt (subject, artistic style, lighting, composition).",
				},
				"image_url": map[string]interface{}{
					"type":        "string",
					"description": "Optional reference image URL (HTTP/HTTPS link or base64 data URL) for image-to-image or style reference.",
				},
			},
			"required": []interface{}{"prompt"},
		},
	}
}

// generateMusicToolDef 是 generate_music 工具的定义。
func generateMusicToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "generate_music",
		"description": "Generate original music and sound effects (~30s) synchronously using Google DeepMind Lyria model. " +
			"Note: Usually takes 1-2 minutes. For environments with proxy/network timeouts (e.g. Cloudflare 100s limit), use 'create_music_task' and 'get_music_status' instead.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "Music prompt describing genre, instruments, mood, tempo/BPM (e.g. 'relaxing lo-fi hip hop with acoustic guitar').",
				},
			},
			"required": []interface{}{"prompt"},
		},
	}
}

// createMusicTaskToolDef 是 create_music_task 工具的定义。
func createMusicTaskToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "create_music_task",
		"description": "Create an asynchronous original music generation task (~30s track) using Google DeepMind Lyria model. " +
			"Returns a job ID immediately (within ~100ms) to avoid HTTP/Cloudflare 100s proxy timeouts. " +
			"Use tool 'get_music_status' to poll for completion (usually takes 1-2 minutes). Requires a Google account cookie.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "Music prompt describing genre, instruments, mood, tempo/BPM (e.g. 'relaxing lo-fi hip hop with acoustic guitar').",
				},
			},
			"required": []interface{}{"prompt"},
		},
	}
}

// getMusicStatusToolDef 是 get_music_status 工具的定义。
func getMusicStatusToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "get_music_status",
		"description": "Poll the status of an asynchronous music task created with 'create_music_task'. " +
			"When completed, returns the audio streaming/download URL and markdown instructions.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"job_id": map[string]interface{}{
					"type":        "string",
					"description": "The job ID returned from 'create_music_task'.",
				},
			},
			"required": []interface{}{"job_id"},
		},
	}
}

// createVideoTaskToolDef 是 create_video_task 工具的定义。
func createVideoTaskToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "create_video_task",
		"description": "Create an asynchronous video generation task using Google Veo video model. " +
			"Returns a job ID that can be polled using 'get_video_status'. Requires a Google account cookie (Pro account).",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "Cinematic video prompt describing camera motion, subject actions, environment, and visual atmosphere.",
				},
				"model": map[string]interface{}{
					"type":        "string",
					"description": "Video model name, default is 'gemini-video'.",
				},
			},
			"required": []interface{}{"prompt"},
		},
	}
}

// getVideoStatusToolDef 是 get_video_status 工具的定义。
func getVideoStatusToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "get_video_status",
		"description": "Check the status of a video generation task previously submitted via 'create_video_task'.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"job_id": map[string]interface{}{
					"type":        "string",
					"description": "The job ID returned by 'create_video_task'.",
				},
			},
			"required": []interface{}{"job_id"},
		},
	}
}

// mcpWebSearch 执行一次联网搜索，返回"答案 + 来源清单"的文本。
func mcpWebSearch(query string) (string, error) {
	mc, ok := Models[rtCfg().DefaultModel]
	if !ok {
		mc = Models["gemini-3.6-flash"]
	}
	res, err := streamGenerate(query, query, mc, nil, nil)
	if err != nil {
		recordRequest("mcp", "gemini-3.6-flash", query, "", res, 502, err.Error(), false)
		return "", err
	}
	text := extractResponseText(res.Raw)
	if text == "" {
		recordRequest("mcp", "gemini-3.6-flash", query, "", res, 502, "上游没有返回内容", false)
		return "", fmt.Errorf("上游没有返回内容")
	}
	sources := extractGrounding(res.Raw)
	var b strings.Builder
	b.WriteString(text)
	if len(sources) > 0 {
		b.WriteString("\n\n---\nSources:\n")
		for i, s := range sources {
			title := s.Title
			if title == "" {
				title = s.URL
			}
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, title, s.URL)
		}
	}
	recordRequest("mcp", "gemini-3.6-flash", query, b.String(), res, 200, "", false)
	return b.String(), nil
}

// mcpGenerateImage 调用 Nano Banana 生图，返回结构化 JSON 及引导指令。支持带参考图生成。
func mcpGenerateImage(prompt, imageURL, baseURL string) ([]map[string]interface{}, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}
	_, mc, err := resolveModel("gemini-image")
	if err != nil {
		return nil, err
	}

	var images []pendingUpload
	if strings.TrimSpace(imageURL) != "" {
		img, err := materializeImage(strings.TrimSpace(imageURL), "", 0)
		if err != nil {
			return nil, fmt.Errorf("处理参考图片失败: %w", err)
		}
		images = append(images, img)
	}

	_, _, res, err := callGemini(prompt, prompt, mc, nil, images, nil, nil)
	if err != nil {
		recordRequest("mcp", "gemini-image", prompt, "", res, 502, err.Error(), false)
		return nil, err
	}
	if res == nil || len(res.Artifacts) == 0 {
		recordRequest("mcp", "gemini-image", prompt, "", res, 502, "未能生成或获取图片产物", false)
		return nil, fmt.Errorf("未能生成或获取图片产物")
	}

	var contents []map[string]interface{}
	var lastPayloadBytes []byte
	for _, a := range res.Artifacts {
		mime := a.Mime
		if mime == "" {
			mime = "image/jpeg"
		}

		// 1. 保存到静态媒体缓存，分配干净的短链 URL
		mediaID := saveMediaArtifact("img", mime, a.Data)
		imgURL := fmt.Sprintf("%s/v1/media/%s", baseURL, mediaID)

		// 2. 核心架构优化：统一返回结构化 JSON + 强指令引导模型在正文中以 Markdown 呈现，
		// 避免客户端在工具卡片内重复强制渲染大图，同时确保任意客户端均可在最终正文中稳定呈现该图片。
		resultPayload := map[string]interface{}{
			"status":      "success",
			"url":         imgURL,
			"prompt":      prompt,
			"instruction": fmt.Sprintf("Please present the generated image to the user in your response using markdown syntax: ![%s](%s)", prompt, imgURL),
		}
		lastPayloadBytes, _ = json.MarshalIndent(resultPayload, "", "  ")

		contents = append(contents, map[string]interface{}{
			"type": "text",
			"text": string(lastPayloadBytes),
		})
	}
	recordRequest("mcp", "gemini-image", prompt, string(lastPayloadBytes), res, 200, "", false)
	return contents, nil
}

// mcpGenerateMusic 调用 Lyria 音乐生成引擎，返回结构化 JSON 及引导指令。
func mcpGenerateMusic(prompt, baseURL string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt 不能为空")
	}
	_, mc, err := resolveModel("gemini-music")
	if err != nil {
		return "", err
	}
	_, _, res, err := callGemini(prompt, prompt, mc, nil, nil, nil, nil)
	if err != nil {
		recordRequest("mcp", "gemini-music", prompt, "", res, 502, err.Error(), false)
		return "", err
	}
	if res == nil || len(res.Artifacts) == 0 {
		recordRequest("mcp", "gemini-music", prompt, "", res, 502, "未能生成或获取音乐产物", false)
		return "", fmt.Errorf("未能生成或获取音乐产物")
	}
	a := res.Artifacts[0]
	mime := a.Mime
	if mime == "" {
		mime = "audio/mpeg"
	}
	mediaID := saveMediaArtifact("music", mime, a.Data)
	audioURL := fmt.Sprintf("%s/v1/media/%s", baseURL, mediaID)

	resultPayload := map[string]interface{}{
		"status":           "success",
		"url":              audioURL,
		"duration_seconds": 30,
		"instruction":      fmt.Sprintf("Please provide the audio link to the user in your response using markdown: [点击在线试听/下载音频](%s)", audioURL),
	}
	payloadBytes, _ := json.MarshalIndent(resultPayload, "", "  ")
	recordRequest("mcp", "gemini-music", prompt, string(payloadBytes), res, 200, "", false)
	return string(payloadBytes), nil
}

// mcpCreateMusicTask 创建后台异步音乐生成任务（防超时）。
func mcpCreateMusicTask(prompt, baseURL string) (map[string]interface{}, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}
	if !hasCookie() {
		return nil, fmt.Errorf("gemini-music is unavailable without a Google account cookie")
	}
	j := &musicJob{
		ID:        "music_" + randHex(16),
		Object:    "music",
		Model:     "gemini-music",
		Status:    "queued",
		CreatedAt: time.Now().Unix(),
		Prompt:    prompt,
	}
	putMusicJob(j)
	go runMusicJob(j)
	return map[string]interface{}{
		"job_id":     j.ID,
		"status":     j.Status,
		"created_at": j.CreatedAt,
		"message":    fmt.Sprintf("Music generation task created. Use tool 'get_music_status' with job_id '%s' to poll for completion (usually takes 1-2 minutes).", j.ID),
	}, nil
}

// mcpGetMusicStatus 查询音乐任务状态。
func mcpGetMusicStatus(jobID, baseURL string) (map[string]interface{}, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("job_id 不能为空")
	}
	j := getMusicJob(jobID)
	if j == nil {
		return nil, fmt.Errorf("music job not found: %s", jobID)
	}
	musicJobsMu.Lock()
	status := j.Status
	errStr := j.Error
	hasMP3 := len(j.mp3) > 0
	mediaID := j.mediaID
	musicJobsMu.Unlock()

	result := map[string]interface{}{
		"job_id":     j.ID,
		"status":     status,
		"model":      j.Model,
		"created_at": j.CreatedAt,
	}
	if errStr != "" {
		result["error"] = errStr
	}
	if status == "completed" && hasMP3 {
		var audioURL string
		if mediaID != "" {
			audioURL = fmt.Sprintf("%s/v1/media/%s", baseURL, mediaID)
		} else {
			audioURL = fmt.Sprintf("%s/v1/music/%s/content", baseURL, j.ID)
		}
		result["audio_url"] = audioURL
		result["content_url"] = fmt.Sprintf("%s/v1/music/%s/content", baseURL, j.ID)
		result["duration_seconds"] = 30
		result["instruction"] = fmt.Sprintf("Please provide the audio link to the user in your response using markdown: [点击在线试听/下载音频](%s)", audioURL)
		result["message"] = fmt.Sprintf("Music generation completed. Audio URL: %s", audioURL)
	}
	return result, nil
}

// mcpCreateVideoTask 创建后台异步视频生成任务。
func mcpCreateVideoTask(prompt, model string) (map[string]interface{}, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}
	if model == "" {
		model = "gemini-video"
	}
	if base, ok := Models[model]; !ok || base.Tool != toolVideo {
		return nil, fmt.Errorf("model must be a video model (gemini-video)")
	}
	if !hasCookie() {
		return nil, fmt.Errorf("gemini-video is unavailable without a Google account cookie (Pro account required)")
	}
	j := &videoJob{
		ID:        "video_" + randHex(16),
		Object:    "video",
		Model:     model,
		Status:    "queued",
		CreatedAt: time.Now().Unix(),
		Prompt:    prompt,
	}
	putVideoJob(j)
	go runVideoJob(j)
	return map[string]interface{}{
		"job_id":     j.ID,
		"status":     j.Status,
		"created_at": j.CreatedAt,
		"message":    fmt.Sprintf("Video generation task created. Use tool 'get_video_status' with job_id '%s' to poll for completion (usually takes 1-3 minutes).", j.ID),
	}, nil
}

// mcpGetVideoStatus 查询视频任务状态。
func mcpGetVideoStatus(jobID, baseURL string) (map[string]interface{}, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("job_id 不能为空")
	}
	j := getVideoJob(jobID)
	if j == nil {
		return nil, fmt.Errorf("video job not found: %s", jobID)
	}
	videoJobsMu.Lock()
	status := j.Status
	errStr := j.Error
	hasMP4 := len(j.mp4) > 0
	mp4Size := len(j.mp4)
	videoJobsMu.Unlock()

	result := map[string]interface{}{
		"job_id":     j.ID,
		"status":     status,
		"model":      j.Model,
		"created_at": j.CreatedAt,
	}
	if errStr != "" {
		result["error"] = errStr
	}
	if status == "completed" && hasMP4 {
		result["content_url"] = fmt.Sprintf("%s/v1/videos/%s/content", baseURL, j.ID)
		result["size_bytes"] = mp4Size
		result["message"] = fmt.Sprintf("Video generation completed. Download MP4 at: %s/v1/videos/%s/content", baseURL, j.ID)
	}
	return result, nil
}

func toolTextError(msg string) map[string]interface{} {
	return map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": msg}},
		"isError": true,
	}
}
