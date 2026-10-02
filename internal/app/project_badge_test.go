package app

import (
	"encoding/xml"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func projectBadgeFixture(t *testing.T, a *App, p Project) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.DB.Exec("INSERT INTO projects(id,name,url,currency,target_minor,active,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", p.ID, p.Name, p.URL, p.Currency, p.TargetMinor, p.Active, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func projectBadgeDonation(t *testing.T, a *App, project string, amount int64, currency, status string, paid time.Time) string {
	t.Helper()
	tx, err := a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	d := Donation{ID: randomToken(), ProjectID: project, StatusToken: "private-status-token", AmountMinor: amount, Currency: currency, MethodID: "private-method-id", MethodType: "custom", MethodName: "private-method-name", Name: "private-donor-name", Email: "private-email@example.com", Message: "private-message", Status: status, Source: "manual", Reference: "private-reference", CreatedAt: paid.UTC().Format(time.RFC3339Nano), PaidAt: paid.UTC().Format(time.RFC3339Nano), DonorUserID: "private-account-id"}
	if err = insertDonation(tx, d); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func projectBadgeSVG(t *testing.T, body string) (title, description string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	metadata := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return title, description
		}
		if err != nil {
			t.Fatalf("invalid SVG XML: %v", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if metadata != "" {
				t.Fatalf("nested element in SVG %s: %s", metadata, element.Name.Local)
			}
			if !contains([]string{"svg", "title", "desc", "rect", "style", "g", "text", "path"}, element.Name.Local) {
				t.Fatalf("active or unexpected SVG element: %s", element.Name.Local)
			}
			for _, attr := range element.Attr {
				if strings.HasPrefix(attr.Name.Local, "on") || attr.Name.Local == "href" {
					t.Fatalf("active or external SVG attribute: %s", attr.Name.Local)
				}
			}
			switch element.Name.Local {
			case "title", "desc":
				metadata = element.Name.Local
			}
		case xml.CharData:
			if metadata == "title" {
				title += string(element)
			} else if metadata == "desc" {
				description += string(element)
			}
		case xml.EndElement:
			if element.Name.Local == metadata {
				metadata = ""
			}
		}
	}
}

func TestProjectBadgeUsesLifetimeConfirmedProjectTotalWithoutPrivateData(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a)
	now := time.Date(2026, 10, 5, 4, 5, 6, 120000000, time.UTC)
	projectBadgeFixture(t, a, Project{ID: "magicnet", Name: "MagicNet", URL: "https://example.com/magicnet", Currency: "USD", TargetMinor: 10000, Active: true})
	projectBadgeFixture(t, a, Project{ID: "other", Name: "Other project", URL: "https://example.com/other", Currency: "USD", TargetMinor: 20000, Active: true})
	for _, fixture := range []struct {
		project, currency, status string
		amount                    int64
		paid                      time.Time
	}{
		{"magicnet", "USD", "confirmed", 500, now.Add(-40 * 24 * time.Hour)},
		{"magicnet", "USD", "confirmed", 2000, now.Add(-time.Hour)},
		{"magicnet", "USD", "confirmed", 550, now},
		{"magicnet", "USD", "confirmed", 1100, now.Add(time.Nanosecond)},
		{"other", "USD", "confirmed", 6000, now.Add(-time.Hour)},
		{"", "USD", "confirmed", 900, now.Add(-time.Hour)},
		{"magicnet", "JPY", "confirmed", 1200, now.Add(-time.Hour)},
		{"magicnet", "USD", "pending", 7000, now.Add(-time.Hour)},
		{"magicnet", "USD", "refunded", 8000, now.Add(-time.Hour)},
		{"magicnet", "USD", "cancelled", 9000, now.Add(-time.Hour)},
	} {
		projectBadgeDonation(t, a, fixture.project, fixture.amount, fixture.currency, fixture.status, fixture.paid)
	}
	malformed := projectBadgeDonation(t, a, "magicnet", 9900, "USD", "confirmed", now.Add(-time.Hour))
	if _, err := a.DB.Exec("UPDATE donations SET paid_at='not-a-payment-time' WHERE id=?", malformed); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"project=magicnet", "project=magicnet&period=all&currency=USD"} {
		rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?"+query, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		title, desc := projectBadgeSVG(t, rr.Body.String())
		if title != "MagicNet" || !strings.Contains(desc, "USD 30.50 / USD 100.00. 30.5%. Donations: 3. All time.") {
			t.Fatalf("wrong project lifetime total: title=%q desc=%q", title, desc)
		}
		for _, private := range []string{"private-", "donor_user_id", "status_token", "provider_ref", "Other project", "https://example.com", s.StatsToken, s.Webhook.Secret, s.SMTP.Password} {
			if strings.Contains(rr.Body.String(), private) {
				t.Fatalf("project SVG exposed private or unrelated data: %q", private)
			}
		}
	}
	// Selecting a project must not silently change the generic site's aggregate.
	generic := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg", nil, nil)
	expectStatus(t, generic, http.StatusOK)
	if !strings.Contains(badgeDescription(t, generic.Body.String()), "Donation amount: USD 99.50. Donations: 5.") {
		t.Fatalf("project selection changed the generic aggregate: %s", generic.Body.String())
	}
}

func TestProjectBadgeParameterValidationAndArchivePrivacy(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC()
	projectBadgeFixture(t, a, Project{ID: "jpy", Name: "日本語のプロジェクト", Currency: "JPY", TargetMinor: 1500, Active: true})
	projectBadgeFixture(t, a, Project{ID: "archived", Name: "Hidden archived name", Currency: "USD", TargetMinor: 12345, Active: false})
	projectBadgeDonation(t, a, "jpy", 750, "JPY", "confirmed", now.Add(-time.Hour))
	for _, query := range []string{"project=jpy", "project=jpy&currency=JPY", "project=jpy&period=all"} {
		rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?"+query, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		if !strings.Contains(badgeDescription(t, rr.Body.String()), "JPY 750 / JPY 1,500. 50.0%.") {
			t.Fatalf("project did not default to its own currency: %s", rr.Body.String())
		}
	}
	for _, query := range []string{"project=", "project=jpy&project=jpy", "project=BadSlug", "project=-jpy", "project=jpy-", "project=" + strings.Repeat("a", 65), "project=jpy&currency=", "project=jpy&currency=USD", "project=jpy&period=", "project=jpy&period=7d", "project=jpy&period=30d", "project=jpy&period=year", "project=jpy&currency=JPY&currency=JPY", "project=jpy&period=all&period=all"} {
		rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?"+query, nil, nil)
		expectStatus(t, rr, http.StatusBadRequest)
		if rr.Header().Get("Cache-Control") != "no-store" || strings.Contains(rr.Header().Get("Content-Type"), "svg") {
			t.Fatal("invalid project badge was cached or rendered as SVG")
		}
	}
	for _, id := range []string{"missing", "archived"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rr := requestJSON(t, badgeHandler(a, now), method, "/badge.svg?project="+id, nil, map[string]string{"If-None-Match": "*"})
			expectStatus(t, rr, http.StatusNotFound)
			if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("ETag") != "" || strings.Contains(rr.Body.String(), "Hidden archived") {
				t.Fatal("unknown or archived project exposed data or a cache validator")
			}
		}
	}
}

func TestProjectBadgeCacheTracksOnlyVisibleProjectChanges(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC()
	projectBadgeFixture(t, a, Project{ID: "magicnet", Name: "MagicNet", Currency: "USD", TargetMinor: 10000, Active: true})
	projectBadgeFixture(t, a, Project{ID: "other", Name: "Other", Currency: "USD", TargetMinor: 10000, Active: true})
	paidID := projectBadgeDonation(t, a, "magicnet", 2500, "USD", "confirmed", now.Add(-time.Hour))
	path := "/badge.svg?project=magicnet&animation=steam"
	handler := badgeHandler(a, now)
	first := requestJSON(t, handler, http.MethodGet, path, nil, nil)
	expectStatus(t, first, http.StatusOK)
	etag := first.Header().Get("ETag")
	head := requestJSON(t, handler, http.MethodHead, path, nil, nil)
	expectStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("ETag") != etag || head.Header().Get("Content-Length") != strconv.Itoa(first.Body.Len()) {
		t.Fatal("project HEAD did not match GET")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		cached := requestJSON(t, handler, method, path, nil, map[string]string{"If-None-Match": `"other", W/` + etag})
		expectStatus(t, cached, http.StatusNotModified)
		if cached.Body.Len() != 0 || cached.Header().Get("ETag") != etag {
			t.Fatal("project conditional request changed the validator or wrote a body")
		}
	}
	projectBadgeDonation(t, a, "other", 7000, "USD", "confirmed", now.Add(-time.Minute))
	projectBadgeDonation(t, a, "magicnet", 9000, "USD", "pending", now.Add(-time.Minute))
	unchanged := requestJSON(t, handler, http.MethodGet, path, nil, map[string]string{"If-None-Match": etag})
	expectStatus(t, unchanged, http.StatusNotModified)
	// Refunding the selected project's payment removes it from the goal total.
	if _, err := a.DB.Exec("UPDATE donations SET status='refunded' WHERE id=?", paidID); err != nil {
		t.Fatal(err)
	}
	refunded := requestJSON(t, handler, http.MethodGet, path, nil, map[string]string{"If-None-Match": etag})
	expectStatus(t, refunded, http.StatusOK)
	if refunded.Header().Get("ETag") == etag || !strings.Contains(badgeDescription(t, refunded.Body.String()), "USD 0.00 / USD 100.00. 0.0%. Donations: 0.") {
		t.Fatal("refund did not invalidate the project SVG and remove its amount")
	}
	etag = refunded.Header().Get("ETag")
	if _, err := a.DB.Exec("UPDATE projects SET name='Renamed',target_minor=20000 WHERE id='magicnet'"); err != nil {
		t.Fatal(err)
	}
	renamed := requestJSON(t, handler, http.MethodGet, path, nil, map[string]string{"If-None-Match": etag})
	expectStatus(t, renamed, http.StatusOK)
	if renamed.Header().Get("ETag") == etag || !strings.Contains(badgeDescription(t, renamed.Body.String()), "Renamed. Donation amount: USD 0.00 / USD 200.00") {
		t.Fatal("project name and target did not change the SVG validator")
	}
	if _, err := a.DB.Exec("UPDATE projects SET active=0 WHERE id='magicnet'"); err != nil {
		t.Fatal(err)
	}
	archived := requestJSON(t, handler, http.MethodGet, path, nil, map[string]string{"If-None-Match": renamed.Header().Get("ETag")})
	expectStatus(t, archived, http.StatusNotFound)
}

func TestProjectBadgeRendersAllLayoutsEscapedAndOverfundedWithoutClippingMoney(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC()
	name := strings.Repeat("界", 60) + `<script onload="x">&`
	projectBadgeFixture(t, a, Project{ID: "large", Name: name, URL: "https://example.com/private-path", Currency: "USD", TargetMinor: 1, Active: true})
	projectBadgeDonation(t, a, "large", math.MaxInt64-1, "USD", "confirmed", now.Add(-time.Hour))
	projectBadgeDonation(t, a, "large", 100, "USD", "confirmed", now.Add(-time.Hour))
	for _, layout := range []string{"receipt", "compact"} {
		for _, theme := range []string{"dark", "light", "transparent"} {
			for _, width := range []string{"240", "480", "1200"} {
				q := url.Values{"project": {"large"}, "layout": {layout}, "theme": {theme}, "width": {width}, "animation": {"steam"}}
				rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?"+q.Encode(), nil, nil)
				expectStatus(t, rr, http.StatusOK)
				title, desc := projectBadgeSVG(t, rr.Body.String())
				if title != name || !strings.Contains(desc, "USD 92,233,720,368,547,758.07 / USD 0.01. 100.0%. Donations: 2.") {
					t.Fatalf("large or overfunded project text was truncated: %q %q", title, desc)
				}
				if !strings.Contains(rr.Body.String(), `width="`+width+`"`) || !strings.Contains(rr.Body.String(), `aria-valuenow="100.0"`) || !strings.Contains(rr.Body.String(), "prefers-reduced-motion:reduce") || strings.Contains(rr.Body.String(), "private-path") {
					t.Fatal("project SVG lost width, progress cap, reduced motion, or exposed its URL")
				}
				// The smallest supported badge must still display the complete
				// raised amount legibly rather than squeezing it into a half-column.
				decoder := xml.NewDecoder(strings.NewReader(rr.Body.String()))
				foundAmount, foundFont := false, false
				for {
					token, err := decoder.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if element, ok := token.(xml.StartElement); ok && element.Name.Local == "text" {
						var value string
						if err = decoder.DecodeElement(&value, &element); err != nil {
							t.Fatal(err)
						}
						if value != "USD 92,233,720,368,547,758.07" {
							continue
						}
						foundAmount = true
						for _, attr := range element.Attr {
							if attr.Name.Local == "font-size" {
								font, err := strconv.ParseFloat(attr.Value, 64)
								if err != nil || math.IsNaN(font) || math.IsInf(font, 0) || font <= 0 {
									t.Fatalf("invalid amount font size: %q", attr.Value)
								}
								foundFont = true
								outputWidth, _ := strconv.ParseFloat(width, 64)
								viewBoxWidth := 480.0
								if layout == "compact" {
									viewBoxWidth = 440
								}
								if font*outputWidth/viewBoxWidth < 10 {
									t.Fatalf("complete amount became unreadably small: layout=%s width=%s font=%s", layout, width, attr.Value)
								}
							}
						}
					}
				}
				if !foundAmount || !foundFont {
					t.Fatal("project SVG omitted the visible full-precision amount or its font size")
				}
				if theme == "transparent" && strings.Contains(rr.Body.String(), "<rect") {
					t.Fatal("transparent project SVG painted a background")
				}
			}
		}
	}
	custom := `<script onload="x">&"'`
	rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?project=large&title="+url.QueryEscape(custom), nil, nil)
	expectStatus(t, rr, http.StatusOK)
	title, desc := projectBadgeSVG(t, rr.Body.String())
	if title != custom || !strings.Contains(desc, name) {
		t.Fatal("custom title replaced or failed to preserve the project's real name")
	}
	bad := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?project=large&title="+strings.Repeat("x", 41), nil, nil)
	expectStatus(t, bad, http.StatusBadRequest)
}

func TestProjectBadgeKeepsFullTargetAtMaximumIntegerPrecision(t *testing.T) {
	for _, layout := range []string{"receipt", "compact"} {
		o, err := parseBadgeOptions("layout="+layout+"&width=240", Site{Currency: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		body := string(renderProjectBadge(o, Project{Name: "Project", Currency: "USD", TargetMinor: math.MaxInt64, RaisedMinor: 1234, Count: 1, Progress: ProjectProgress(1234, math.MaxInt64)}))
		_, desc := projectBadgeSVG(t, body)
		if !strings.Contains(desc, "USD 12.34 / USD 92,233,720,368,547,758.07") || !strings.Contains(body, ">/ USD 92,233,720,368,547,758.07</text>") {
			t.Fatalf("project SVG truncated its maximum target: %s", body)
		}
	}
}

func TestProjectBadgeProgressBarDefensivelyClampsInvalidProgress(t *testing.T) {
	o, err := parseBadgeOptions("", Site{Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		progress float64
		want     string
		end      string
	}{{-1, "0.0", "24.00"}, {math.NaN(), "0.0", "24.00"}, {math.Inf(1), "0.0", "24.00"}, {125, "100.0", "456.00"}, {12.34, "12.3", "77.31"}} {
		body := string(renderProjectBadge(o, Project{Name: "Project", Currency: "USD", TargetMinor: 10000, RaisedMinor: 1234, Count: 1, Progress: fixture.progress}))
		projectBadgeSVG(t, body)
		if !strings.Contains(body, `aria-valuenow="`+fixture.want+`"`) || strings.Contains(body, "NaN") || strings.Contains(body, "+Inf") || strings.Contains(body, "-Inf") {
			t.Fatalf("unsafe progress rendering: %s", body)
		}
		decoder := xml.NewDecoder(strings.NewReader(body))
		lastPath := ""
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if element, ok := token.(xml.StartElement); ok && element.Name.Local == "path" {
				for _, attr := range element.Attr {
					if attr.Name.Local == "d" {
						lastPath = attr.Value
					}
				}
			}
		}
		if !strings.HasPrefix(lastPath, "M24.00 ") || !strings.HasSuffix(lastPath, "H"+fixture.end) {
			t.Fatalf("progress %.2f painted the wrong extent: %s", fixture.progress, lastPath)
		}
	}
}
