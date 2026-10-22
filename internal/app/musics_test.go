package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMusicHTTPItem(t *testing.T) {
	fakeJob := &musicJob{
		ID:        "music_http_test",
		Object:    "music",
		Model:     "gemini-music",
		Status:    "completed",
		CreatedAt: time.Now().Unix(),
		Prompt:    "cyberpunk synthwave",
		mp3:       []byte("fake_mp3_data_stream"),
		mime:      "audio/mpeg",
	}
	putMusicJob(fakeJob)

	// 1. GET /v1/music/{id}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/music/music_http_test", nil)
	handleMusicItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if res["id"] != "music_http_test" || res["status"] != "completed" {
		t.Errorf("unexpected status/id: %v", res)
	}

	// 2. GET /v1/music/{id}/content
	recContent := httptest.NewRecorder()
	reqContent := httptest.NewRequest("GET", "/v1/music/music_http_test/content", nil)
	handleMusicItem(recContent, reqContent)
	if recContent.Code != http.StatusOK {
		t.Fatalf("expected 200 for content, got %d", recContent.Code)
	}
	if recContent.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("expected audio/mpeg, got %s", recContent.Header().Get("Content-Type"))
	}
	if !bytes.Equal(recContent.Body.Bytes(), fakeJob.mp3) {
		t.Errorf("content body mismatch")
	}

	// 3. GET unknown job
	recNotFound := httptest.NewRecorder()
	reqNotFound := httptest.NewRequest("GET", "/v1/music/music_not_exists", nil)
	handleMusicItem(recNotFound, reqNotFound)
	if recNotFound.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown job, got %d", recNotFound.Code)
	}
}
