package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestAnnouncementOptionalAndLocalized(t *testing.T) {
	s := defaults()
	if s.Site.Announcement != "" || s.Site.AnnouncementURL != "" {
		t.Fatal("defaults must not populate an announcement")
	}
	if err := validateSettings(s); err != nil {
		t.Fatal(err)
	}
	s.Site.Announcement = "A thank-you page is coming."
	if err := validateSettings(s); err != nil {
		t.Fatal("text-only announcement rejected:", err)
	}
	s.Site.Announcement = ""
	s.Site.AnnouncementURL = "https://example.com/thanks?lang=en#supporters"
	s.Site.Translations["en"] = Translation{Announcement: "See the thank-you page."}
	if err := validateSettings(s); err != nil {
		t.Fatal("localized announcement with shared HTTPS link rejected:", err)
	}

}

func TestAnnouncementRejectsUnsafeLinksAndOrphanedLinks(t *testing.T) {
	for _, link := range []string{
		"javascript:alert(1)", "data:text/html,<script>alert(1)</script>", "http://example.com/thanks",
		"//example.com/thanks", "/thanks", "https:example.com", "https://user:password@example.com/thanks",
		"https://example.com\\@evil.example/thanks", " https://example.com/thanks", "https://example.com/thanks\n",
	} {
		t.Run(link, func(t *testing.T) {
			s := defaults()
			s.Site.Announcement = "Thanks!"
			s.Site.AnnouncementURL = link
			if err := validateSettings(s); err == nil {
				t.Fatal("unsafe announcement link accepted")
			}
		})
	}
	s := defaults()
	s.Site.Announcement = " \n\t"
	s.Site.Translations["en"] = Translation{Announcement: " "}
	s.Site.AnnouncementURL = "https://example.com/thanks"
	if err := validateSettings(s); err == nil {
		t.Fatal("link without visible base or translated text accepted")
	}
}

func TestAnnouncementTextAndLinkBounds(t *testing.T) {
	for _, modify := range []func(*Settings){
		func(s *Settings) { s.Site.Announcement = strings.Repeat("x", 2001) },
		func(s *Settings) {
			s.Site.Translations["zh-TW"] = Translation{Announcement: strings.Repeat("中", 667)}
		},
		func(s *Settings) {
			s.Site.Announcement = "Thanks!"
			s.Site.AnnouncementURL = "https://example.com/" + strings.Repeat("x", 2048)
		},
	} {
		s := defaults()
		modify(&s)
		if err := validateSettings(s); err == nil {
			t.Fatal("oversized announcement configuration accepted")
		}
	}
}

func TestAnnouncementSettingsPreserveConfiguredPrivacy(t *testing.T) {
	a := testApp(t)
	s, err := a.settings()
	if err != nil {
		t.Fatal(err)
	}
	s.Site.Privacy = "Maintainer-written privacy policy."
	s.Site.Translations["en"] = Translation{Privacy: "Custom English policy.", Announcement: "<3 Thanks!"}
	s.Site.Announcement = "<b>Literal text, not markup</b>"
	s.Site.AnnouncementURL = "https://example.com/thanks"
	if err := validateSettings(s); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/admin/settings", a.putSettings)
	expectStatus(t, requestJSON(t, mux, http.MethodPut, "/api/admin/settings", s, nil), http.StatusOK)
	got, err := a.settings()
	if err != nil || got.Site.Privacy != s.Site.Privacy || got.Site.Translations["en"].Privacy != s.Site.Translations["en"].Privacy || got.Site.Announcement != s.Site.Announcement {
		t.Fatal("saving announcement changed configured privacy or plain text", err)
	}
	public := decodeResponse[map[string]any](t, requestJSON(t, a.Routes(), http.MethodGet, "/api/site", nil, nil))
	if public["announcement"] != s.Site.Announcement || public["announcement_url"] != s.Site.AnnouncementURL {
		t.Fatal("public site configuration lost the announcement or shared link")
	}
	s.Site.AnnouncementURL = "javascript:alert(1)"
	expectStatus(t, requestJSON(t, mux, http.MethodPut, "/api/admin/settings", s, nil), http.StatusBadRequest)
	got, err = a.settings()
	if err != nil || got.Site.AnnouncementURL != "https://example.com/thanks" || got.Site.Privacy != "Maintainer-written privacy policy." {
		t.Fatal("rejected announcement settings changed persisted configuration", err)
	}
}
