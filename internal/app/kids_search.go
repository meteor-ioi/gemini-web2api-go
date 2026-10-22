package app

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type KidsSearchResultItem struct {
	URL   string `json:"url"`
	Thumb string `json:"thumb"`
	Title string `json:"title"`
}

type KidsSearchResponse struct {
	Query  string                 `json:"query"`
	First  int                    `json:"first"`
	Count  int                    `json:"count"`
	Images []KidsSearchResultItem `json:"images"`
}

var bingImgRegexp = regexp.MustCompile(`m="({.*?})"`)

func handleKidsImageSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		handleOptions(w, r)
		return
	}
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 200, KidsSearchResponse{Images: []KidsSearchResultItem{}})
		return
	}

	first := 1
	if fStr := r.URL.Query().Get("first"); fStr != "" {
		if f, err := strconv.Atoi(fStr); err == nil && f > 0 {
			first = f
		}
	}

	bingURL := "https://cn.bing.com/images/async?q=" + url.QueryEscape(q) +
		"&first=" + strconv.Itoa(first) + "&count=35&adlt=strict&mmasync=1"

	req, err := http.NewRequest("GET", bingURL, nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "failed to search bing images: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to read body"})
		return
	}

	matches := bingImgRegexp.FindAllSubmatch(bodyBytes, -1)
	var items []KidsSearchResultItem
	seen := make(map[string]bool)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rawJSON := html.UnescapeString(string(m[1]))
		var data struct {
			MURL string `json:"murl"`
			TURL string `json:"turl"`
			T    string `json:"t"`
		}
		if err := json.Unmarshal([]byte(rawJSON), &data); err == nil && data.MURL != "" {
			if !seen[data.MURL] {
				seen[data.MURL] = true
				thumb := data.TURL
				if thumb == "" {
					thumb = data.MURL
				}
				cleanTitle := strings.ReplaceAll(data.T, "\ue000", "")
				cleanTitle = strings.ReplaceAll(cleanTitle, "\ue001", "")
				items = append(items, KidsSearchResultItem{
					URL:   data.MURL,
					Thumb: thumb,
					Title: cleanTitle,
				})
			}
		}
	}

	writeJSON(w, 200, KidsSearchResponse{
		Query:  q,
		First:  first,
		Count:  len(items),
		Images: items,
	})
}
