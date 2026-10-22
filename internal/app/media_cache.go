package app

import (
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

type mediaItem struct {
	ID        string
	Mime      string
	Data      []byte
	CreatedAt int64
}

var (
	mediaCacheMu sync.RWMutex
	mediaCache   = make(map[string]*mediaItem)
)

// saveMediaArtifact 将生成的媒体数据存入轻量内存缓存，并返回可直接访问的唯一短 ID。
func saveMediaArtifact(prefix, mime string, data []byte) string {
	ext := ".bin"
	switch {
	case strings.Contains(mime, "png"):
		ext = ".png"
	case strings.Contains(mime, "jpeg") || strings.Contains(mime, "jpg"):
		ext = ".jpg"
	case strings.Contains(mime, "webp"):
		ext = ".webp"
	case strings.Contains(mime, "audio/mpeg") || strings.Contains(mime, "mp3"):
		ext = ".mp3"
	case strings.Contains(mime, "audio/wav") || strings.Contains(mime, "wav"):
		ext = ".wav"
	case strings.Contains(mime, "video/mp4") || strings.Contains(mime, "mp4"):
		ext = ".mp4"
	}

	id := fmt.Sprintf("%s_%s%s", prefix, randHex(12), ext)
	item := &mediaItem{
		ID:        id,
		Mime:      mime,
		Data:      data,
		CreatedAt: time.Now().Unix(),
	}

	mediaCacheMu.Lock()
	// 清理 24 小时前的旧数据，防止长期运行占用内存
	cutoff := time.Now().Unix() - 24*3600
	for k, v := range mediaCache {
		if v.CreatedAt < cutoff {
			delete(mediaCache, k)
		}
	}
	mediaCache[id] = item
	mediaCacheMu.Unlock()

	return id
}

// getMediaArtifact 根据 ID 获取缓存的媒体数据。
func getMediaArtifact(id string) *mediaItem {
	mediaCacheMu.RLock()
	defer mediaCacheMu.RUnlock()
	return mediaCache[id]
}

// handleMediaGet 处理 GET /v1/media/{id}，提供免鉴权公网/内网图片音频访问直链。
func handleMediaGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := path.Base(r.URL.Path)
	item := getMediaArtifact(id)
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "media artifact not found or expired"})
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", item.Mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(item.Data)
	}
}

// getBaseURLFromRequest 从 HTTP 请求中提取外部访问的基础 URL（如 https://gemini.ioiabc.cc.cd）
func getBaseURLFromRequest(r *http.Request) string {
	proto := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		proto = "https"
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = xfh
	}
	if host == "" {
		host = "localhost:8083"
	}
	return fmt.Sprintf("%s://%s", proto, host)
}
