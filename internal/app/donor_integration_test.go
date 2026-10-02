package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type donorFixture struct {
	id, token, csrf string
}

func donorSessionFixture(t *testing.T, a *App) donorFixture {
	t.Helper()
	d := donorFixture{id: randomToken(), token: randomToken(), csrf: randomToken()}
	if _, err := a.DB.Exec("INSERT INTO donor_users(id,display_name,created_at) VALUES(?,?,?)", d.id, "Shared display name", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec("INSERT INTO donor_sessions(token_hash,user_id,csrf,expires) VALUES(?,?,?,?)", digest(d.token), d.id, d.csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d donorFixture) headers(key string) map[string]string {
	return map[string]string{"Cookie": "donate_donor=" + d.token, "X-CSRF-Token": d.csrf, "Idempotency-Key": key}
}

func donorCheckout(t *testing.T, a *App, d donorFixture, in donationInput, key string) checkoutResponse {
	t.Helper()
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, d.headers(key))
	expectStatus(t, rr, http.StatusOK)
	return decodeResponse[checkoutResponse](t, rr)
}

type donorHistory struct {
	Donations []donorDonation `json:"donations"`
	Total     int             `json:"total"`
	Limit     int             `json:"limit"`
	Offset    int             `json:"offset"`
}

func TestDonorCheckoutUsesSessionAndHistoryCannotClaimMatchingGuestDetails(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner, other := donorSessionFixture(t, a), donorSessionFixture(t, a)
	in := donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", Name: "Same supporter", Email: "same@example.com", Message: "Private donor message", AcceptedTerms: true}
	raw, _ := json.Marshal(in)
	var forged map[string]any
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	forged["donor_user_id"] = other.id
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", forged, owner.headers(""))
	expectStatus(t, rr, http.StatusOK)
	owned := decodeResponse[checkoutResponse](t, rr)
	d, err := a.donation(owned.ID)
	if err != nil || d.DonorUserID != owner.id {
		t.Fatalf("checkout trusted a client identity: %#v, %v", d, err)
	}
	// A matching email, name, or forged ID never makes an anonymous donation an
	// authenticated donor's property.
	forged["donor_user_id"] = owner.id
	rr = requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", forged, nil)
	expectStatus(t, rr, http.StatusOK)
	guest := decodeResponse[checkoutResponse](t, rr)
	d, err = a.donation(guest.ID)
	if err != nil || d.DonorUserID != "" {
		t.Fatalf("guest identity was claimed from body or PII: %#v, %v", d, err)
	}
	others := donorCheckout(t, a, other, in, "")
	expectStatus(t, confirmCustom(t, a, owned.ID, map[string]string{}), http.StatusOK)
	for _, fixture := range []struct {
		user donorFixture
		id   string
	}{{owner, owned.ID}, {other, others.ID}} {
		rr = requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, fixture.user.headers(""))
		expectStatus(t, rr, http.StatusOK)
		history := decodeResponse[donorHistory](t, rr)
		if history.Total != 1 || history.Limit != 20 || history.Offset != 0 || len(history.Donations) != 1 || history.Donations[0].ID != fixture.id {
			t.Fatalf("history mixed account or guest records: %#v", history)
		}
		for _, private := range []string{owned.StatusToken, others.StatusToken, guest.StatusToken, guest.ID, "same@example.com", "Same supporter", "Private donor message", "donor_user_id", "checkout_url", "provider_ref"} {
			if strings.Contains(rr.Body.String(), private) {
				t.Fatalf("donor history exposed a secret or unrelated record %q: %s", private, rr.Body.String())
			}
		}
		if fixture.user.id == owner.id && history.Donations[0].Status != "confirmed" {
			t.Fatal("manual confirmation lost account association")
		}
	}
	// Empty pages keep an empty array, with pagination bounded independently of
	// the administrator's larger export/list limit.
	rr = requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations?limit=999&offset=1", nil, owner.headers(""))
	expectStatus(t, rr, http.StatusOK)
	history := decodeResponse[donorHistory](t, rr)
	if history.Total != 1 || history.Limit != 100 || history.Offset != 1 || history.Donations == nil || len(history.Donations) != 0 {
		t.Fatalf("history pagination not bounded: %#v", history)
	}
	for _, headers := range []map[string]string{nil, {"Cookie": "donate_session=" + owner.token, "X-CSRF-Token": owner.csrf}} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, headers), http.StatusUnauthorized)
	}
	adminToken, adminCSRF := randomToken(), randomToken()
	if _, err = a.DB.Exec("INSERT INTO auth_sessions(token_hash,csrf,level,expires,generation) SELECT ?,?,'admin',?,generation FROM auth_state WHERE id=1", digest(adminToken), adminCSRF, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	adminHeaders := map[string]string{"Cookie": "donate_session=" + adminToken, "X-CSRF-Token": adminCSRF}
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/admin/settings", nil, adminHeaders), http.StatusOK)
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, adminHeaders), http.StatusUnauthorized)
	for _, target := range []string{"/api/admin/settings", "/api/admin/donations", "/api/admin/export"} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, target, nil, owner.headers("")), http.StatusUnauthorized)
	}
	for _, target := range []string{"/api/site", "/api/stats", "/api/donations/" + owned.ID + "?token=" + owned.StatusToken} {
		rr = requestJSON(t, a.Routes(), http.MethodGet, target, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		for _, forbidden := range []string{owner.id, other.id, "donor_user_id", "same@example.com"} {
			if strings.Contains(rr.Body.String(), forbidden) {
				t.Fatalf("public endpoint %s leaked account or email %q", target, forbidden)
			}
		}
	}
	// The administrator can distinguish the stable account on its private ledger.
	rr = requestJSON(t, http.HandlerFunc(a.listDonations), http.MethodGet, "/api/admin/donations", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), `"donor_user_id":"`+owner.id+`"`) {
		t.Fatal("private ledger omitted the stable account ID")
	}
}

func TestDonorCheckoutIdempotencyRejectsDifferentIdentityWithoutLeakingCapabilities(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner, other := donorSessionFixture(t, a), donorSessionFixture(t, a)
	in := donationInput{AmountMinor: 1000, Currency: "USD", MethodID: "qr", AcceptedTerms: true}
	key := "donor-idempotency-shared-key-001"
	first := donorCheckout(t, a, owner, in, key)
	second := donorCheckout(t, a, owner, in, key)
	if first.ID != second.ID || first.StatusToken != second.StatusToken {
		t.Fatal("same-account retry changed donation identity or capability")
	}
	for _, headers := range []map[string]string{other.headers(key), {"Idempotency-Key": key}} {
		rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers)
		expectStatus(t, rr, http.StatusConflict)
		for _, forbidden := range []string{first.ID, first.StatusToken} {
			if strings.Contains(rr.Body.String(), forbidden) {
				t.Fatal("cross-account retry leaked the existing donation capability")
			}
		}
	}
	guestKey := "guest-idempotency-before-login-001"
	guest := createCustom(t, a, in, guestKey)
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(guestKey))
	expectStatus(t, rr, http.StatusConflict)
	if strings.Contains(rr.Body.String(), guest.StatusToken) || countRows(t, a, "donations") != 2 {
		t.Fatal("login claimed a guest retry or changed the ledger")
	}
}

func TestDonorCheckoutRequiresCurrentSessionAndItsOwnCSRF(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner := donorSessionFixture(t, a)
	in := donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", AcceptedTerms: true}
	for _, headers := range []map[string]string{
		{"Cookie": "donate_donor=" + owner.token},
		{"Cookie": "donate_donor=" + owner.token, "X-CSRF-Token": "administrator-csrf"},
		{"Cookie": "donate_donor=" + owner.token, "X-CSRF-Token": owner.csrf, "Origin": ""},
		{"Cookie": "donate_donor=" + owner.token, "X-CSRF-Token": owner.csrf, "Origin": "https://other.example"},
	} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers), http.StatusForbidden)
	}
	if _, err := a.DB.Exec("UPDATE donor_sessions SET expires=? WHERE token_hash=?", time.Now().Add(-time.Minute).Unix(), digest(owner.token)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers("")), http.StatusForbidden)
	if countRows(t, a, "donations") != 0 {
		t.Fatal("invalid donor session or CSRF silently created a guest donation")
	}
	// Explicit anonymous checkout remains available after signing out.
	_ = createCustom(t, a, in, "")
}

func TestDonorIdentitySurvivesProviderConfirmationAndDuplicateWebhook(t *testing.T) {
	a := testApp(t)
	m := stripeMethod("card")
	testSettings(t, a, m)
	owner := donorSessionFixture(t, a)
	a.Payments.Client = &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/checkout/sessions" {
			t.Fatalf("unexpected provider request: %s %s", req.Method, req.URL)
		}
		return providerResponse(200, map[string]any{"id": "cs_test_account", "url": "https://checkout.stripe.com/c/pay/account"}), nil
	})}
	checkout := donorCheckout(t, a, owner, donationInput{AmountMinor: 900, Currency: "USD", MethodID: m.ID, AcceptedTerms: true}, "donor-provider-checkout-0001")
	before, err := a.donation(checkout.ID)
	if err != nil || before.DonorUserID != owner.id || before.Status != "pending" {
		t.Fatalf("checkout lost donor identity: %#v, %v", before, err)
	}
	event := stripeEvent("evt_account_confirmed", "checkout.session.completed", stripePaidObject(before))
	for range 2 {
		expectStatus(t, stripeWebhook(t, a, m.ID, event, m.Config["webhook_secret"]), http.StatusOK)
	}
	after, err := a.donation(checkout.ID)
	if err != nil || after.DonorUserID != owner.id || after.Status != "confirmed" || countRows(t, a, "notification_jobs") != 1 {
		t.Fatalf("provider confirmation changed identity or duplicated event: %#v, %v", after, err)
	}
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, owner.headers(""))
	expectStatus(t, rr, http.StatusOK)
	history := decodeResponse[donorHistory](t, rr)
	if history.Total != 1 || len(history.Donations) != 1 || history.Donations[0].Status != "confirmed" {
		t.Fatalf("provider settlement missing in own history: %#v", history)
	}
}

func TestDonorMigrationPreservesLegacyDonationsAdministratorAndGuestRetry(t *testing.T) {
	dataDir := t.TempDir()
	assets := fstest.MapFS{"index.html": {Data: []byte("Donate")}, "admin.html": {Data: []byte("Admin")}}
	a, err := New(dataDir, "http://localhost:8080", assets)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if a != nil {
			_ = a.Close()
		}
	}()
	testSettings(t, a, customMethod())
	in := donationInput{AmountMinor: 1234, Currency: "USD", MethodID: "qr", Email: "legacy@example.com", AcceptedTerms: true}
	key := "legacy-guest-idempotency-key-001"
	checkout := createCustom(t, a, in, key)
	before, err := a.donation(checkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("UPDATE auth_state SET password_enabled=0,password_hash=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	savedCredential := []byte("untouched existing administrator credential")
	if _, err = a.DB.Exec("INSERT INTO auth_credentials(id,data,active,generation) SELECT ?,?,1,generation FROM auth_state WHERE id=1", "existing-admin-key", savedCredential); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("DROP INDEX donation_donor_history; ALTER TABLE donations DROP COLUMN donor_user_id; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = New(dataDir, "http://localhost:8080", assets)
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.donation(checkout.ID)
	if err != nil || before != after || after.DonorUserID != "" {
		t.Fatalf("legacy migration changed a donation: %#v / %#v, %v", before, after, err)
	}
	var credential []byte
	var enabled bool
	var version int
	if err = a.DB.QueryRow("SELECT data FROM auth_credentials WHERE id='existing-admin-key'").Scan(&credential); err != nil || !bytes.Equal(credential, savedCredential) {
		t.Fatalf("migration changed existing administrator key: %v", err)
	}
	if err = a.DB.QueryRow("SELECT password_enabled FROM auth_state WHERE id=1").Scan(&enabled); err != nil || enabled {
		t.Fatalf("migration enabled password authentication: %v", err)
	}
	if err = a.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema migration not recorded: %d, %v", version, err)
	}
	retried := createCustom(t, a, in, key)
	if retried != checkout || countRows(t, a, "donations") != 1 {
		t.Fatal("migration invalidated a previous guest checkout fingerprint")
	}
}

func TestPublicOriginNormalizesDefaultPortsWithoutMergingDifferentOrigins(t *testing.T) {
	for _, test := range []struct {
		configured, browser, foreign string
	}{
		{"https://Donate.Example:443/", "https://donate.example", "https://donate.example:8443"},
		{"http://localhost:80", "http://localhost", "http://localhost:8080"},
		{"https://donate.example:8443", "https://donate.example:8443", "https://donate.example"},
	} {
		t.Run(test.configured, func(t *testing.T) {
			a, err := New(t.TempDir(), test.configured, fstest.MapFS{})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if a.PublicURL != test.browser {
				t.Fatalf("public origin = %q, want %q", a.PublicURL, test.browser)
			}
			for _, origin := range []string{test.browser, test.foreign} {
				r := httptest.NewRequest(http.MethodPost, "/api/donations", nil)
				r.Header.Set("Origin", origin)
				want := origin == test.browser
				if a.originOK(r) != want || a.Donors.CheckOrigin(r) != want {
					t.Fatalf("origin %q accepted differently by App and donor auth", origin)
				}
			}
			// The administrator must receive the same canonical origin rather
			// than retaining a default port from the original environment value.
			password, err := a.Auth.Password()
			if err != nil {
				t.Fatal(err)
			}
			rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/auth/password", map[string]string{"password": password}, map[string]string{"Origin": test.browser})
			expectStatus(t, rr, http.StatusOK)
			rr = requestJSON(t, a.Routes(), http.MethodPost, "/api/auth/password", map[string]string{"password": password}, map[string]string{"Origin": test.foreign})
			expectStatus(t, rr, http.StatusForbidden)
		})
	}
	// Passkey constructors reject IP RPs, but origin formatting still preserves
	// valid IPv6 brackets when it removes a default port.
	u, err := url.Parse("https://[2001:db8::1]:443")
	if err != nil || normalizedPublicOrigin(u) != "https://[2001:db8::1]" {
		t.Fatalf("IPv6 origin lost its brackets: %v", err)
	}
}
