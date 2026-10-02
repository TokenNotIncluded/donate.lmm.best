package app

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func badgeHandler(a *App, now time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /badge.svg", func(w http.ResponseWriter, r *http.Request) { a.publicBadgeAt(w, r, now) })
	return mux
}

func badgeFixture(t *testing.T, a *App, amount int64, currency, status string, paid time.Time) {
	t.Helper()
	tx, err := a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	d := Donation{ID: randomToken(), StatusToken: "private-status-token", AmountMinor: amount, Currency: currency, MethodID: "private-method-id", MethodType: "custom", MethodName: "private-method-name", Name: "private-donor-name", Email: "private-email@example.com", Message: "private-message", Status: status, Source: "manual", Reference: "private-reference", CreatedAt: paid.UTC().Format(time.RFC3339Nano), PaidAt: paid.UTC().Format(time.RFC3339Nano), DonorUserID: "private-account-id"}
	if err = insertDonation(tx, d); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func badgeDescription(t *testing.T, body string) string {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	var desc string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid SVG XML: %v; body=%s", err, body)
		}
		if element, ok := token.(xml.StartElement); ok && element.Name.Local == "desc" {
			if err := decoder.DecodeElement(&desc, &element); err != nil {
				t.Fatal(err)
			}
		}
	}
	return desc
}

func TestBadgeAggregatesConfirmedDonationsInOneCurrencyWithoutPrivateDetails(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a)
	now := time.Date(2026, 10, 3, 4, 5, 6, 0, time.UTC)
	for _, fixture := range []struct {
		amount       int64
		curr, status string
	}{{1234, "USD", "confirmed"}, {1, "USD", "confirmed"}, {9999, "USD", "pending"}, {8888, "USD", "refunded"}, {7777, "USD", "cancelled"}, {1200, "JPY", "confirmed"}} {
		badgeFixture(t, a, fixture.amount, fixture.curr, fixture.status, now.Add(-time.Hour))
	}
	handler := badgeHandler(a, now)
	for _, fixture := range []struct{ query, amount, count string }{{"", "USD 12.35", "2"}, {"?currency=JPY", "JPY 1,200", "1"}, {"?currency=EUR", "EUR 0.00", "0"}} {
		rr := requestJSON(t, handler, http.MethodGet, "/badge.svg"+fixture.query, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		desc := badgeDescription(t, rr.Body.String())
		if !strings.Contains(desc, "Donation amount: "+fixture.amount+".") || !strings.Contains(desc, "Donations: "+fixture.count+".") {
			t.Fatalf("wrong money/count aggregate: %s", desc)
		}
		for _, private := range []string{"private-", "donor_user_id", "status_token", "provider_ref", s.StatsToken, s.Webhook.Secret, s.SMTP.Password} {
			if strings.Contains(rr.Body.String(), private) {
				t.Fatalf("SVG exposed private data %q", private)
			}
		}
	}
}

func TestBadgePeriodsUseInclusiveUTCNanosecondBoundariesAndExcludeFuturePayments(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 5, 6, 120000000, time.FixedZone("UTC+8", 8*60*60))
	for _, period := range []string{"all", "7d", "30d", "year"} {
		t.Run(period, func(t *testing.T) {
			a := testApp(t)
			start, _ := badgePeriod(period, "en", now)
			if start.IsZero() {
				start = now.UTC().Add(-365 * 24 * time.Hour)
			}
			for _, fixture := range []struct {
				amount int64
				paid   time.Time
			}{{3, start.Add(-time.Nanosecond)}, {5, start}, {7, start.Add(time.Nanosecond)}, {11, now}, {13, now.Add(time.Nanosecond)}} {
				badgeFixture(t, a, fixture.amount, "USD", "confirmed", fixture.paid)
			}
			rr := requestJSON(t, badgeHandler(a, now), http.MethodGet, "/badge.svg?period="+period, nil, nil)
			expectStatus(t, rr, http.StatusOK)
			want := "Donation amount: USD 0.23. Donations: 3."
			if period == "all" {
				want = "Donation amount: USD 0.26. Donations: 4."
			}
			if !strings.Contains(badgeDescription(t, rr.Body.String()), want) {
				t.Fatalf("period included wrong boundary records: %s", rr.Body.String())
			}
		})
	}
	// New Year's day in UTC+8 can still belong to the previous UTC year.
	start, label := badgePeriod("year", "en", time.Date(2026, 1, 1, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60)))
	if start != time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) || label != "2025 · UTC" {
		t.Fatalf("year boundary used a local timezone: %v, %s", start, label)
	}
}

func TestBadgeMoneyFormattingPreservesIntegerPrecisionAndCurrencyExponent(t *testing.T) {
	for _, test := range []struct {
		amount         int64
		currency, want string
	}{
		{1, "USD", "USD 0.01"}, {100, "USD", "USD 1.00"}, {123456789, "EUR", "EUR 1,234,567.89"}, {1, "JPY", "JPY 1"}, {9007199254740993, "USD", "USD 90,071,992,547,409.93"}, {9223372036854775807, "USD", "USD 92,233,720,368,547,758.07"},
	} {
		if got := badgeAmount(test.amount, test.currency); got != test.want {
			t.Fatalf("amount = %s, want %s", got, test.want)
		}
	}
}

func TestBadgeValidatesParametersAndProducesSelfContainedEscapedSVG(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC()
	handler := badgeHandler(a, now)
	for _, query := range []string{"currency=BTC", "lang=ja", "period=month", "layout=chart", "theme=green", "animation=script", "width=239", "width=1201", "width=1.5", "title=%ZZ", "title=%FF", "title=one&title=two", "title=" + url.QueryEscape(strings.Repeat("界", 41)), "amount_label=" + url.QueryEscape(strings.Repeat("界", 25)), "count_label=bad%0Aline", "title=%00", "title=" + url.QueryEscape("hidden\u202e")} {
		rr := requestJSON(t, handler, http.MethodGet, "/badge.svg?"+query, nil, nil)
		expectStatus(t, rr, http.StatusBadRequest)
		if rr.Header().Get("Cache-Control") != "no-store" || strings.Contains(rr.Header().Get("Content-Type"), "svg") {
			t.Fatal("invalid badge request was cached or served as SVG")
		}
	}
	injected := `<script onload="alert(1)">&"'`
	for _, layout := range []string{"receipt", "compact"} {
		for _, theme := range []string{"dark", "light", "transparent"} {
			q := url.Values{"layout": {layout}, "theme": {theme}, "width": {"240"}, "title": {injected}, "amount_label": {strings.Repeat("金", 24)}, "count_label": {strings.Repeat("筆", 24)}, "animation": {"steam"}, "lang": {"zh-TW"}}
			rr := requestJSON(t, handler, http.MethodGet, "/badge.svg?"+q.Encode(), nil, nil)
			expectStatus(t, rr, http.StatusOK)
			desc := badgeDescription(t, rr.Body.String())
			if !strings.Contains(desc, strings.Repeat("金", 24)) || !strings.Contains(desc, strings.Repeat("筆", 24)) || !strings.Contains(rr.Body.String(), `width="240"`) || !strings.Contains(rr.Body.String(), "prefers-reduced-motion:reduce") {
				t.Fatal("badge lost labels, width, or reduced-motion support")
			}
			decoder := xml.NewDecoder(strings.NewReader(rr.Body.String()))
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if element, ok := token.(xml.StartElement); ok {
					if !contains([]string{"svg", "title", "desc", "rect", "style", "g", "text", "path"}, element.Name.Local) {
						t.Fatalf("unwanted SVG element: %s", element.Name.Local)
					}
					for _, attr := range element.Attr {
						if strings.HasPrefix(attr.Name.Local, "on") || attr.Name.Local == "href" {
							t.Fatalf("active or external SVG attribute: %s", attr.Name.Local)
						}
					}
				}
			}
			if theme == "transparent" && strings.Contains(rr.Body.String(), "<rect") {
				t.Fatal("transparent theme painted a background")
			}
		}
	}
	for _, lang := range []struct{ query, label string }{{"zh", "捐赠金额"}, {"zh-CN", "捐赠笔数"}, {"zh-TW", "捐贈筆數"}, {"en", "Donations"}} {
		rr := requestJSON(t, handler, http.MethodGet, "/badge.svg?lang="+lang.query, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		if !strings.Contains(badgeDescription(t, rr.Body.String()), lang.label) {
			t.Fatal("badge language mapping incorrect")
		}
	}
}

func TestBadgeCacheValidatorsHEADAndContentChanges(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC()
	handler := badgeHandler(a, now)
	first := requestJSON(t, handler, http.MethodGet, "/badge.svg", nil, nil)
	expectStatus(t, first, http.StatusOK)
	etag := first.Header().Get("ETag")
	if len(etag) != 66 || first.Header().Get("Cache-Control") != "public, max-age=300" || first.Header().Get("Content-Type") != "image/svg+xml; charset=utf-8" {
		t.Fatalf("invalid SVG cache headers: %v", first.Header())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, candidate := range []string{etag, "W/" + etag, `"not-current", W/` + etag, "*"} {
			rr := requestJSON(t, handler, method, "/badge.svg", nil, map[string]string{"If-None-Match": candidate})
			expectStatus(t, rr, http.StatusNotModified)
			if rr.Body.Len() != 0 || rr.Header().Get("ETag") != etag {
				t.Fatal("304 wrote a body or changed its validator")
			}
		}
	}
	head := requestJSON(t, handler, http.MethodHead, "/badge.svg", nil, nil)
	expectStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("ETag") != etag || head.Header().Get("Content-Length") != strconv.Itoa(first.Body.Len()) {
		t.Fatal("HEAD differed from GET")
	}
	badgeFixture(t, a, 99, "USD", "confirmed", now.Add(-time.Hour))
	updated := requestJSON(t, handler, http.MethodGet, "/badge.svg", nil, map[string]string{"If-None-Match": etag})
	expectStatus(t, updated, http.StatusOK)
	if updated.Header().Get("ETag") == etag {
		t.Fatal("confirmed total did not invalidate the SVG")
	}
	for _, query := range []string{"?title=Other", "?currency=JPY", "?layout=compact", "?theme=light", "?width=1200", "?animation=steam"} {
		rr := requestJSON(t, handler, http.MethodGet, "/badge.svg"+query, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		if rr.Header().Get("ETag") == updated.Header().Get("ETag") {
			t.Fatalf("changed badge option %s kept the validator", query)
		}
	}
}
