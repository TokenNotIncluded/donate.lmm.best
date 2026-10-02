package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestLegalPagesUseCurrentConfiguredCopyAndTranslationsWithoutExposingSettings(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a, customMethod())
	s.Site.DefaultLanguage = "zh-TW"
	s.Site.Terms = "Base terms\n\nNo promised benefits."
	s.Site.Privacy = "Base privacy"
	s.Site.Translations = map[string]Translation{
		"en":    {Terms: "English terms", Privacy: "English privacy"},
		"zh-TW": {Terms: "繁體條款", Privacy: "繁體隱私"},
	}
	if err := a.saveSettings(s); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, lang, copy string }{
		{"/terms", "zh-TW", "繁體條款"},
		{"/privacy", "zh-TW", "繁體隱私"},
		{"/terms?lang=en", "en", "English terms"},
		{"/privacy?lang=en", "en", "English privacy"},
		{"/terms?lang=zh-CN", "zh-CN", "Base terms"},
		{"/privacy?lang=zh-CN", "zh-CN", "Base privacy"},
		{"/terms?lang=unavailable", "zh-TW", "繁體條款"},
	} {
		t.Run(test.path, func(t *testing.T) {
			rr := requestJSON(t, a.Routes(), http.MethodGet, test.path, nil, nil)
			expectStatus(t, rr, http.StatusOK)
			if rr.Header().Get("Content-Type") != "text/html; charset=utf-8" || rr.Header().Get("Cache-Control") != "no-cache" || rr.Header().Get("Content-Language") != test.lang || !strings.Contains(rr.Body.String(), `<html lang="`+test.lang+`">`) || !strings.Contains(rr.Body.String(), test.copy) {
				t.Fatalf("incorrect language, cache, or legal copy: headers=%v body=%s", rr.Header(), rr.Body.String())
			}
			for _, private := range []string{s.StatsToken, s.Webhook.Secret, s.SMTP.Password, "notification-secret", "bootstrap", "status_token"} {
				if strings.Contains(rr.Body.String(), private) {
					t.Fatalf("public legal page exposed private settings: %q", private)
				}
			}
		})
	}
	// Updates are read from settings for every request, rather than compiled
	// policies or stale HTML cached before the administrator edited them.
	s.Site.Translations["en"] = Translation{Terms: "Updated English terms"}
	if err := a.saveSettings(s); err != nil {
		t.Fatal(err)
	}
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/terms?lang=en", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), "<p>Updated English terms</p>") || strings.Contains(rr.Body.String(), "<p>English terms</p>") {
		t.Fatal("policy update was not reflected")
	}
	rr = requestJSON(t, a.Routes(), http.MethodHead, "/privacy?lang=en", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Language") != "en" {
		t.Fatal("HEAD policy request rendered a body or lost language headers")
	}
}

func TestLegalPagesEscapeAdministratorTextAndOnlyOfferConfiguredLanguages(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a)
	s.Site.Languages = []string{"en"}
	s.Site.DefaultLanguage = "en"
	s.Site.Translations["en"] = Translation{Terms: `<script>alert("policy")</script>` + "\n\n" + `<a href="javascript:alert(1)">link & text</a>`}
	if err := a.saveSettings(s); err != nil {
		t.Fatal(err)
	}
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/terms?lang=zh-TW", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	body := rr.Body.String()
	if rr.Header().Get("Content-Language") != "en" || strings.Contains(body, "<script>") || strings.Contains(body, `<a href="javascript:`) || !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "link &amp; text") {
		t.Fatalf("configured legal text was interpreted as HTML or disabled language was used: %s", body)
	}
	if strings.Count(body, "<p>") != 2 || !strings.Contains(body, `href="/privacy?lang=en"`) || !strings.Contains(body, `aria-current="page"`) || strings.Contains(body, `href="/terms?lang=zh-TW"`) {
		t.Fatal("legal page paragraph or language navigation is incorrect")
	}
}
