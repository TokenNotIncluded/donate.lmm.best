package app

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

func marshalSite(site Site) (map[string]any, error) {
	raw, e := json.Marshal(site)
	if e != nil {
		return nil, e
	}
	var v map[string]any
	e = json.Unmarshal(raw, &v)
	return v, e
}
func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024+65536)
	if e := r.ParseMultipartForm(2 * 1024 * 1024); e != nil {
		fail(w, 400, "请选择不超过 2MB 的 PNG、JPEG 或 WebP 图片")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, e := r.FormFile("file")
	if e != nil {
		fail(w, 400, "请选择图片")
		return
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
	if e != nil || len(raw) > 2*1024*1024 || len(raw) < 12 {
		fail(w, 400, "图片无效或超过 2MB")
		return
	}
	kind := http.DetectContentType(raw)
	ext := ""
	switch kind {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	default:
		fail(w, 400, "仅支持 PNG、JPEG 或 WebP 图片")
		return
	}
	// Decode headers to reject malformed images and oversized decompression dimensions.
	cfg, _, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 32000000 {
		fail(w, 400, "图片无法读取，或分辨率过大")
		return
	}
	if _, _, e = image.Decode(bytes.NewReader(raw)); e != nil {
		fail(w, 400, "图片文件不完整或已损坏")
		return
	}
	name := randomToken() + ext
	path := filepath.Join(a.DataDir, "uploads", name)
	if e = os.WriteFile(path, raw, 0600); e != nil {
		internalError(w)
		return
	}
	respond(w, 201, map[string]string{"url": "/uploads/" + name})
}
func (a *App) serveUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	ext := filepath.Ext(name)
	if !validID(strings.TrimSuffix(name, ext)) || (ext != ".png" && ext != ".jpg" && ext != ".webp") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	http.ServeFile(w, r, filepath.Join(a.DataDir, "uploads", name))
}
