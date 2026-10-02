package app

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

var legalPage = template.Must(template.New("legal").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>{{.Title}} · Donate</title>
{{if .Favicon}}<link rel="icon" href="/favicon.svg" type="image/svg+xml">{{end}}
<style>
*{box-sizing:border-box}body{margin:0;background:#0b0b0b;color:#f5f5f5;font:15px/1.9 system-ui,sans-serif}main{width:min(100% - 40px,640px);margin:48px auto}header{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px;margin-bottom:40px}header>a{font-size:23px;text-decoration:none}nav{display:flex;gap:18px;flex-wrap:wrap;font-size:12px}a{color:inherit;text-underline-offset:5px}a:focus-visible{outline:2px solid #fff;outline-offset:5px}nav a{color:#a3a3a3;min-height:44px;display:inline-flex;align-items:center}nav a[aria-current],nav a:hover{color:#fff}h1{font-size:24px;font-weight:500;margin:0 0 24px}p{margin:0 0 22px;color:#d4d4d4;white-space:pre-wrap;overflow-wrap:anywhere}footer{margin-top:36px;font-size:13px;color:#a3a3a3}footer a{display:inline-flex;align-items:center;min-height:44px}
</style>
</head>
<body><main>
<header><a href="/" aria-label="{{.Back}}">Donate</a><nav aria-label="{{.LanguageLabel}}">{{range .Languages}}<a href="{{.URL}}" lang="{{.Lang}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav></header>
<h1>{{.Title}}</h1>
{{range .Paragraphs}}<p>{{.}}</p>{{end}}
<footer><a href="{{.OtherURL}}">{{.OtherTitle}}</a></footer>
</main></body>
</html>`))

type legalLanguage struct {
	Lang, Label, URL string
	Current          bool
}

type legalPageData struct {
	Lang, Title, Back, LanguageLabel string
	OtherURL, OtherTitle             string
	Paragraphs                       []string
	Languages                        []legalLanguage
	Favicon                          bool
}

func (a *App) legal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	s, err := a.settings()
	if err != nil {
		internalError(w)
		return
	}
	lang := r.URL.Query().Get("lang")
	if !supportedLanguages[lang] || !contains(s.Site.Languages, lang) {
		lang = s.Site.DefaultLanguage
	}
	if !supportedLanguages[lang] {
		lang = "en"
	}
	labels := map[string][4]string{
		"zh-CN": {"用户协议", "隐私政策", "返回捐赠", "语言"},
		"zh-TW": {"使用者協議", "隱私權政策", "返回捐贈", "語言"},
		"en":    {"Terms", "Privacy", "Back to Donate", "Language"},
	}[lang]
	terms := r.URL.Path == "/terms"
	text, title, otherTitle, otherPath := s.Site.Privacy, labels[1], labels[0], "/terms"
	if terms {
		text, title, otherTitle, otherPath = s.Site.Terms, labels[0], labels[1], "/privacy"
	}
	translation := s.Site.Translations[lang]
	localized := translation.Privacy
	if terms {
		localized = translation.Terms
	}
	if localized != "" {
		text = localized
	}
	data := legalPageData{Lang: lang, Title: title, Back: labels[2], LanguageLabel: labels[3], OtherTitle: otherTitle, OtherURL: otherPath + "?lang=" + lang, Paragraphs: []string{}, Languages: []legalLanguage{}}
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(paragraph) != "" {
			data.Paragraphs = append(data.Paragraphs, paragraph)
		}
	}
	for _, available := range s.Site.Languages {
		label := map[string]string{"zh-CN": "简体中文", "zh-TW": "繁體中文", "en": "English"}[available]
		if label != "" {
			data.Languages = append(data.Languages, legalLanguage{Lang: available, Label: label, URL: r.URL.Path + "?lang=" + available, Current: available == lang})
		}
	}
	if a.assets != nil {
		if _, err := fs.Stat(a.assets, "favicon.svg"); err == nil {
			data.Favicon = true
		}
	}
	var body bytes.Buffer
	if err = legalPage.Execute(&body, data); err != nil {
		internalError(w)
		return
	}
	content, err := a.versionStaticHTML(body.Bytes())
	if err != nil {
		internalError(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}
