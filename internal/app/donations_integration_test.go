package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCustomDonationRequiresTermsAndCapabilityAndConfirmation(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	in := donationInput{AmountMinor: 1500, Currency: "USD", MethodID: "qr", Name: "Supporter", Email: "private@example.com", Message: "Keep going", Public: true}
	routes := a.Routes()
	rr := requestJSON(t, routes, http.MethodPost, "/api/donations", in, nil)
	expectStatus(t, rr, http.StatusBadRequest)
	if countRows(t, a, "donations") != 0 {
		t.Fatal("a request without agreement created a donation")
	}

	checkout := createCustom(t, a, in, "")
	if countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("unverified custom payment generated a success notification")
	}
	for _, token := range []string{"", "wrong-token"} {
		rr = requestJSON(t, routes, http.MethodGet, "/api/donations/"+checkout.ID+"?token="+token, nil, nil)
		expectStatus(t, rr, http.StatusNotFound)
	}
	rr = requestJSON(t, routes, http.MethodGet, "/api/donations/"+checkout.ID+"?token="+checkout.StatusToken, nil, nil)
	expectStatus(t, rr, http.StatusOK)
	status := decodeResponse[map[string]any](t, rr)
	if status["status"] != "pending" || status["amount_minor"] != float64(1500) {
		t.Fatalf("unexpected pending status: %v", status)
	}
	for _, private := range []string{"private@example.com", "Supporter", "Keep going", checkout.StatusToken} {
		if strings.Contains(rr.Body.String(), private) {
			t.Fatalf("status API leaked donor details or capability: %q", private)
		}
	}

	paidAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	rr = confirmCustom(t, a, checkout.ID, map[string]string{"reference": "bank-statement-42", "paid_at": paidAt})
	expectStatus(t, rr, http.StatusOK)
	rr = confirmCustom(t, a, checkout.ID, map[string]string{"reference": "duplicate-does-not-overwrite"})
	expectStatus(t, rr, http.StatusOK)
	d, err := a.donation(checkout.ID)
	if err != nil || d.Status != "confirmed" || d.Reference != "bank-statement-42" || d.PaidAt != paidAt {
		t.Fatalf("confirmation was not stable: %#v, %v", d, err)
	}
	if countRows(t, a, "notification_jobs") != 1 {
		t.Fatal("custom confirmation did not create exactly one notification")
	}
	var payload []byte
	if err := a.DB.QueryRow("SELECT payload FROM notification_jobs").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type     string         `json:"type"`
		Donation map[string]any `json:"donation"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "donation.confirmed" || event.Donation["email"] != "private@example.com" || event.Donation["paid_at"] != paidAt || event.Donation["amount_minor"] != float64(1500) || event.Donation["method_id"] != "qr" {
		t.Fatalf("notification lost required donation details: %s", payload)
	}
	if strings.Contains(string(payload), "secret") || strings.Contains(string(payload), checkout.StatusToken) {
		t.Fatalf("notification payload leaked credentials: %s", payload)
	}
}

func TestDonationIdempotencyReusesCapabilityAndRejectsDifferentRequest(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	in := donationInput{AmountMinor: 1200, Currency: "USD", MethodID: "qr", AcceptedTerms: true}
	key := "checkout-retry-request-0001"
	first := createCustom(t, a, in, key)
	second := createCustom(t, a, in, key)
	if first.ID != second.ID || first.StatusToken != second.StatusToken || countRows(t, a, "donations") != 1 {
		t.Fatal("retry duplicated the donation or changed its capability")
	}
	in.AmountMinor++
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, map[string]string{"Idempotency-Key": key})
	expectStatus(t, rr, http.StatusConflict)
	if countRows(t, a, "donations") != 1 {
		t.Fatal("conflicting idempotency reuse modified the ledger")
	}
}

func TestPublicSiteAndStatsExcludePrivateDonorsAndSecretsAndKeepCurrenciesSeparate(t *testing.T) {
	a := testApp(t)
	card := stripeMethod("card")
	card.Config["secret_key"] = "sk_test_provider-secret-never-public"
	s := testSettings(t, a, customMethod(), card)
	publicUSD := createCustom(t, a, donationInput{AmountMinor: 1234, Currency: "USD", Name: "Public supporter", Email: "usd-private@example.com", Message: "Visible thanks", Public: true}, "")
	privateUSD := createCustom(t, a, donationInput{AmountMinor: 4321, Currency: "USD", Name: "Private supporter", Email: "private@example.com", Message: "Private message"}, "")
	publicJPY := createCustom(t, a, donationInput{AmountMinor: 500, Currency: "JPY", Name: "JPY supporter", Email: "jpy-private@example.com", Public: true}, "")
	_ = createCustom(t, a, donationInput{AmountMinor: 9999, Currency: "USD", Name: "Pending supporter", Public: true}, "")
	for _, checkout := range []checkoutResponse{publicUSD, privateUSD, publicJPY} {
		expectStatus(t, confirmCustom(t, a, checkout.ID, map[string]string{}), http.StatusOK)
	}
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/site", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	body := rr.Body.String()
	for _, forbidden := range []string{"usd-private@example.com", "jpy-private@example.com", "private@example.com", "Private supporter", "Private message", "Pending supporter", s.StatsToken, s.Webhook.Secret, s.SMTP.Password, "provider-secret-never-public", publicUSD.StatusToken} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public site leaked %q: %s", forbidden, body)
		}
	}
	var site struct {
		Recent  []map[string]any `json:"recent"`
		Stats   Stats            `json:"stats"`
		Presets []int64          `json:"presets"`
	}
	site = decodeResponse[struct {
		Recent  []map[string]any `json:"recent"`
		Stats   Stats            `json:"stats"`
		Presets []int64          `json:"presets"`
	}](t, rr)
	if len(site.Recent) != 2 || site.Stats.Count != 2 || site.Stats.TotalMinor != 5555 || site.Stats.Currency != "USD" || len(site.Presets) != 4 || site.Presets[0] != 5 {
		t.Fatalf("invalid public totals, consent, or major-unit presets: %#v", site)
	}
	rr = requestJSON(t, a.Routes(), http.MethodGet, "/api/stats?currency=JPY", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	stats := decodeResponse[Stats](t, rr)
	if stats.Count != 1 || stats.TotalMinor != 500 || stats.Currency != "JPY" || len(stats.ByCurrency) != 2 || len(stats.Methods) != 2 || len(stats.Daily) != 2 {
		t.Fatalf("stats added different currencies or changed JPY exponent: %#v", stats)
	}
	for _, c := range stats.ByCurrency {
		if c.Currency == "USD" && (c.Count != 2 || c.TotalMinor != 5555) || c.Currency == "JPY" && (c.Count != 1 || c.TotalMinor != 500) {
			t.Fatalf("wrong currency subtotal: %#v", c)
		}
	}
	for _, token := range []string{"", "wrong-token"} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/private/stats", nil, map[string]string{"Authorization": "Bearer " + token}), http.StatusUnauthorized)
	}
	rr = requestJSON(t, a.Routes(), http.MethodGet, "/api/private/stats", nil, map[string]string{"Authorization": "Bearer " + s.StatsToken})
	expectStatus(t, rr, http.StatusOK)
	if strings.Contains(rr.Body.String(), "example.com") || strings.Contains(rr.Body.String(), "supporter") {
		t.Fatal("private statistics token exposed donor PII")
	}
}

func TestDisabledCollectionFieldsAreNotStored(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a, customMethod())
	s.Site.CollectName, s.Site.CollectEmail, s.Site.CollectMessage = false, false, false
	if err := a.saveSettings(s); err != nil {
		t.Fatal(err)
	}
	checkout := createCustom(t, a, donationInput{AmountMinor: 100, Currency: "USD", Name: "Do not collect", Email: "not even valid email", Message: "Do not store"}, "")
	d, err := a.donation(checkout.ID)
	if err != nil || d.Name != "" || d.Email != "" || d.Message != "" {
		t.Fatalf("disabled donor fields were collected: %#v, %v", d, err)
	}
}

func TestManualRecordingRetryDoesNotDuplicateLedgerOrNotifications(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	in := donationInput{AmountMinor: 2500, Currency: "USD", MethodID: "qr", Name: "Manual supporter", Reference: "statement-1"}
	headers := map[string]string{"Idempotency-Key": "manual-record-retry-0001"}
	handler := http.HandlerFunc(a.manualDonation)
	first := requestJSON(t, handler, http.MethodPost, "/api/admin/donations", in, headers)
	expectStatus(t, first, http.StatusCreated)
	second := requestJSON(t, handler, http.MethodPost, "/api/admin/donations", in, headers)
	expectStatus(t, second, http.StatusOK)
	d1, d2 := decodeResponse[Donation](t, first), decodeResponse[Donation](t, second)
	if d1.ID != d2.ID || d1.PaidAt != d2.PaidAt || d1.Source != "manual" || d1.Status != "confirmed" || countRows(t, a, "donations") != 1 || countRows(t, a, "notification_jobs") != 1 {
		t.Fatalf("manual retry duplicated or changed the donation: %#v / %#v", d1, d2)
	}
	in.Reference = "different-statement"
	expectStatus(t, requestJSON(t, handler, http.MethodPost, "/api/admin/donations", in, headers), http.StatusConflict)
	if countRows(t, a, "donations") != 1 || countRows(t, a, "notification_jobs") != 1 {
		t.Fatal("conflicting manual request affected ledger/outbox")
	}
}

func TestPrivateDonationNotificationCarriesTrustedAccountID(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner := donorSessionFixture(t, a)
	checkout := donorCheckout(t, a, owner, donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", AcceptedTerms: true}, "")
	expectStatus(t, confirmCustom(t, a, checkout.ID, map[string]string{}), http.StatusOK)
	var payload []byte
	if err := a.DB.QueryRow("SELECT payload FROM notification_jobs").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Donation map[string]any `json:"donation"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Donation["donor_user_id"] != owner.id {
		t.Fatal("private notification lost trusted account association")
	}
	if strings.Contains(string(payload), owner.token) || strings.Contains(string(payload), checkout.StatusToken) {
		t.Fatal("private notification leaked bearer credentials")
	}
}
