package app

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const sourceURL = "https://github.com/TokenNotIncluded/donate.lmm.best"

type starSnapshot struct {
	Stars     *int      `json:"stars"`
	FetchedAt string    `json:"fetched_at"`
	Stale     bool      `json:"stale"`
	Next      time.Time `json:"-"`
}

func (a *App) sourceInfo(w http.ResponseWriter, r *http.Request) {
	a.starMu.Lock()
	defer a.starMu.Unlock()
	cache := a.starCache
	if time.Now().After(cache.Next) {
		req, e := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/repos/TokenNotIncluded/donate.lmm.best", nil)
		if e == nil {
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("User-Agent", "donate.lmm.best")
			client := *a.Chain.Client
			client.Timeout = 5 * time.Second
			resp, e := client.Do(req)
			if e == nil {
				data, e := io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				var result struct {
					FullName string `json:"full_name"`
					Stars    *int   `json:"stargazers_count"`
				}
				if e == nil && resp.StatusCode == 200 && json.Unmarshal(data, &result) == nil && result.Stars != nil && *result.Stars >= 0 && result.FullName == "TokenNotIncluded/donate.lmm.best" {
					cache = starSnapshot{Stars: result.Stars, FetchedAt: time.Now().UTC().Format(time.RFC3339), Next: time.Now().Add(time.Hour)}
				} else {
					cache.Stale = true
					cache.Next = time.Now().Add(5 * time.Minute)
				}
			} else {
				cache.Stale = true
				cache.Next = time.Now().Add(5 * time.Minute)
			}
		}
		a.starCache = cache
	}
	respond(w, 200, map[string]any{"url": sourceURL, "stars": cache.Stars, "fetched_at": cache.FetchedAt, "stale": cache.Stale, "source": "GitHub API"})
}
