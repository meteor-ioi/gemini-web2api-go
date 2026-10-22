package app

// /v1/music —— 异步音乐生成端点（对齐 /v1/videos 设计）。
//
// gemini 音乐生成（Lyria）需要几十秒到两分钟，同步阻塞 HTTP 请求极易触发 Cloudflare 100 秒超时或客户端超时。
// 因此采用完全对齐 videos 的异步任务机制：
//   POST /v1/music               建任务，立即返回 {id, status:"queued"}
//   GET  /v1/music/{id}          轮询状态 queued|in_progress|completed|failed
//   GET  /v1/music/{id}/content  完成后下载 MP3
// 底层复用 callGemini（跟 model=gemini-music 同一条链），后台 goroutine 跑，前台立即返回 id。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type musicJob struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model"`
	Status    string `json:"status"` // queued | in_progress | completed | failed
	CreatedAt int64  `json:"created_at"`
	Prompt    string `json:"prompt,omitempty"`
	Error     string `json:"error,omitempty"`

	mp3     []byte // 完成后的音频字节，不进 JSON
	mime    string
	mediaID string // 存入 media_cache 的 ID，用于免鉴权直链访问
}

var (
	musicJobs   = map[string]*musicJob{}
	musicJobsMu sync.Mutex
)

func putMusicJob(j *musicJob) {
	musicJobsMu.Lock()
	// 清理 2 小时前的旧任务，避免内存堆积
	cutoff := time.Now().Unix() - 2*3600
	for id, old := range musicJobs {
		if old.CreatedAt < cutoff {
			delete(musicJobs, id)
		}
	}
	musicJobs[j.ID] = j
	musicJobsMu.Unlock()
}

func getMusicJob(id string) *musicJob {
	musicJobsMu.Lock()
	defer musicJobsMu.Unlock()
	return musicJobs[id]
}

// handleCreateMusic 处理 POST /v1/music。
func handleCreateMusic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": map[string]string{"message": "method not allowed"}})
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"message": "bad json body"}})
		return
	}
	prompt, _ := req["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"message": "missing prompt", "type": "invalid_request_error"}})
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		model = "gemini-music"
	}
	if base, ok := Models[model]; !ok || base.Tool != toolMusic {
		writeJSON(w, 400, map[string]any{"error": map[string]string{
			"message": "model must be a music model (gemini-music)", "type": "invalid_request_error"}})
		return
	}
	j := &musicJob{
		ID:        "music_" + randHex(16),
		Object:    "music",
		Model:     model,
		Status:    "queued",
		CreatedAt: time.Now().Unix(),
		Prompt:    prompt,
	}
	putMusicJob(j)
	go runMusicJob(j)
	writeJSON(w, 200, j)
}

// runMusicJob 后台跑音乐生成，结果写回 job。
func runMusicJob(j *musicJob) {
	musicJobsMu.Lock()
	j.Status = "in_progress"
	musicJobsMu.Unlock()

	_, mc, err := resolveModel(j.Model)
	if err != nil {
		finishMusicJob(j, nil, "", "", err.Error())
		return
	}
	_, _, res, err := callGemini(j.Prompt, j.Prompt, mc, nil, nil, nil, nil)
	if err != nil {
		recordRequest("music", j.Model, j.Prompt, "", res, 502, err.Error(), false)
		finishMusicJob(j, nil, "", "", err.Error())
		return
	}
	if res == nil || len(res.Artifacts) == 0 {
		recordRequest("music", j.Model, j.Prompt, "", res, 502, "no music produced", false)
		finishMusicJob(j, nil, "", "", "no music produced")
		return
	}
	a := res.Artifacts[0]
	mime := a.Mime
	if mime == "" {
		mime = "audio/mpeg"
	}
	mediaID := saveMediaArtifact("music", mime, a.Data)
	recordRequest("music", j.Model, j.Prompt, "", res, 200, "", false)
	finishMusicJob(j, a.Data, mime, mediaID, "")
}

func finishMusicJob(j *musicJob, mp3 []byte, mime, mediaID, errStr string) {
	musicJobsMu.Lock()
	defer musicJobsMu.Unlock()
	if errStr != "" {
		j.Status = "failed"
		j.Error = errStr
		return
	}
	j.mp3 = mp3
	j.mime = mime
	if j.mime == "" {
		j.mime = "audio/mpeg"
	}
	j.mediaID = mediaID
	j.Status = "completed"
}

// handleMusicItem 处理 GET /v1/music/{id} 和 /v1/music/{id}/content。
func handleMusicItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"error": map[string]string{"message": "method not allowed"}})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/music/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	j := getMusicJob(id)
	if j == nil {
		writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "music job not found", "type": "not_found"}})
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		musicJobsMu.Lock()
		status, mp3, mime := j.Status, j.mp3, j.mime
		musicJobsMu.Unlock()
		if status != "completed" || len(mp3) == 0 {
			writeJSON(w, 404, map[string]any{"error": map[string]string{
				"message": "music not ready (status=" + status + ")", "type": "not_found"}})
			return
		}
		w.Header().Set("Content-Type", mime)
		w.WriteHeader(200)
		_, _ = w.Write(mp3)
		return
	}
	writeJSON(w, 200, j)
}
