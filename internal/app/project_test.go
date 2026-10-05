package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestProjectAcceptsOtherCurrenciesWithoutMixingGoalProgress(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	p := projectCreateFixture(t, a, "multi-currency", "CNY", 100000, true)
	for _, unit := range []string{"USD", "CNY", "JPY"} {
		rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", donationInput{AmountMinor: 500, Currency: unit, MethodID: "qr", ProjectID: p.ID, AcceptedTerms: true}, nil)
		expectStatus(t, rr, http.StatusOK)
		d := decodeResponse[checkoutResponse](t, rr)
		expectStatus(t, confirmCustom(t, a, d.ID, map[string]string{"reference": "multi-" + unit}), http.StatusOK)
	}
	state, err := a.publicProject(context.Background(), p.ID)
	if err != nil || state.Currency != "CNY" || state.RaisedMinor != 500 || state.Count != 1 || state.Progress != 0.5 || len(state.ByCurrency) != 3 {
		t.Fatalf("unlike currencies contaminated goal: %#v, %v", state, err)
	}
	for _, total := range state.ByCurrency {
		if total.TotalMinor != 500 || total.Count != 1 {
			t.Fatalf("incorrect currency total: %#v", total)
		}
	}
}

func TestCryptoDonationCanFundCNYProject(t *testing.T) {
	f := newCryptoFixture(t)
	p := projectCreateFixture(t, f.a, "crypto-cny", "CNY", 100000, true)
	settings, err := f.a.settings()
	if err != nil {
		t.Fatal(err)
	}
	rr := requestJSON(t, f.a.Routes(), http.MethodPost, "/api/donations", donationInput{AmountMinor: 1000, Currency: "USD", MethodID: "crypto", CryptoNetwork: settings.Crypto.Networks[0].ID, CryptoAsset: "USDT", ProjectID: p.ID, AcceptedTerms: true}, nil)
	expectStatus(t, rr, http.StatusOK)
	d := decodeResponse[checkoutResponse](t, rr)
	stored, err := f.a.donation(d.ID)
	if err != nil || stored.ProjectID != p.ID || stored.Currency != "USD" {
		t.Fatalf("crypto checkout lost its project or denomination: %#v, %v", stored, err)
	}
	state := projectReadFixture(t, f.a, p.ID)
	if state.RaisedMinor != 0 || state.Count != 0 {
		t.Fatal("unpaid USD quote raised CNY goal")
	}
	f.amounts[cryptoHash("a")] = 10000000
	result := f.submit(t, d, cryptoHash("a"))
	if result["status"] != "confirmed" {
		t.Fatalf("fixture did not settle: %v", result)
	}
	settled, err := f.a.publicProject(context.Background(), p.ID)
	if err != nil || settled.RaisedMinor != 0 || settled.Count != 0 || len(settled.ByCurrency) != 1 || settled.ByCurrency[0].Currency != "USD" || settled.ByCurrency[0].TotalMinor != 1000 {
		t.Fatalf("USD chain settlement mixed into CNY goal: %#v, %v", settled, err)
	}
}

type projectResult struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Currency    string  `json:"currency"`
	TargetMinor int64   `json:"target_minor"`
	RaisedMinor int64   `json:"raised_minor"`
	Count       int64   `json:"count"`
	Progress    float64 `json:"progress"`
	Active      bool    `json:"active"`
	DonateURL   string  `json:"donate_url"`
	BadgeURL    string  `json:"badge_url"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Project CRUD tests exercise the handlers directly, while the route-boundary
// test below independently proves that only administrator sessions reach them.
func projectAdminHandler(a *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/projects", a.listAdminProjects)
	mux.HandleFunc("POST /api/admin/projects", a.createProject)
	mux.HandleFunc("PUT /api/admin/projects/{id}", a.updateProject)
	return mux
}

func projectTestInput(id, currency string, target int64, active bool) map[string]any {
	return map[string]any{"id": id, "name": "Project " + id, "url": "https://example.com/" + id, "currency": currency, "target_minor": target, "active": active}
}

func projectCreateFixture(t *testing.T, a *App, id, currency string, target int64, active bool) projectResult {
	t.Helper()
	rr := requestJSON(t, projectAdminHandler(a), http.MethodPost, "/api/admin/projects", projectTestInput(id, currency, target, active), map[string]string{"Idempotency-Key": "project-create-fixture-" + id})
	expectStatus(t, rr, http.StatusCreated)
	return decodeResponse[projectResult](t, rr)
}

func projectReadFixture(t *testing.T, a *App, id string) projectResult {
	t.Helper()
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/projects/"+id, nil, nil)
	expectStatus(t, rr, http.StatusOK)
	return decodeResponse[projectResult](t, rr)
}

func projectUpdateFixture(t *testing.T, a *App, p projectResult, value map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return requestJSON(t, projectAdminHandler(a), http.MethodPut, "/api/admin/projects/"+p.ID, value, nil)
}

func projectAdminSession(t *testing.T, a *App) map[string]string {
	t.Helper()
	token, csrf := randomToken(), randomToken()
	if _, err := a.DB.Exec("INSERT INTO auth_sessions(token_hash,csrf,level,expires,generation) SELECT ?,?,'admin',?,generation FROM auth_state WHERE id=1", digest(token), csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Cookie": "donate_session=" + token, "X-CSRF-Token": csrf}
}

func TestProjectCRUDIsIdempotentAndPublicListsHideArchivedProjects(t *testing.T) {
	a := testApp(t)
	handler := projectAdminHandler(a)
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/projects", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	list := decodeResponse[struct {
		Projects []projectResult `json:"projects"`
	}](t, rr)
	if list.Projects == nil || len(list.Projects) != 0 {
		t.Fatalf("an empty installation must have an empty project array: %#v", list)
	}
	in := projectTestInput("open-source", "USD", 10000, true)
	for _, key := range []string{"", "short", strings.Repeat("x", 81)} {
		expectStatus(t, requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, map[string]string{"Idempotency-Key": key}), http.StatusBadRequest)
	}
	if countRows(t, a, "projects") != 0 {
		t.Fatal("invalid idempotency keys created projects")
	}
	headers := map[string]string{"Idempotency-Key": "project-create-retry-request-0001"}
	first := requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, headers)
	expectStatus(t, first, http.StatusCreated)
	second := requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, headers)
	expectStatus(t, second, http.StatusCreated)
	p, replay := decodeResponse[projectResult](t, first), decodeResponse[projectResult](t, second)
	if p != replay || p.ID != "open-source" || p.Currency != "USD" || p.TargetMinor != 10000 || !p.Active || p.RaisedMinor != 0 || p.Count != 0 || p.Progress != 0 || p.CreatedAt == "" || p.UpdatedAt == "" || countRows(t, a, "projects") != 1 {
		t.Fatalf("project creation/retry changed identity or totals: %#v / %#v", p, replay)
	}
	for _, raw := range []string{p.DonateURL, p.BadgeURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.Host != "localhost:8080" || u.Query().Get("project") != p.ID {
			t.Fatalf("project URL does not point to this site's project: %q, %v", raw, err)
		}
	}
	in["name"] = "Changed retry"
	expectStatus(t, requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, headers), http.StatusConflict)
	expectStatus(t, requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, map[string]string{"Idempotency-Key": "project-duplicate-slug-request-0001"}), http.StatusConflict)
	archived := projectCreateFixture(t, a, "archived", "JPY", 1000, false)
	rr = requestJSON(t, a.Routes(), http.MethodGet, "/api/projects", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	list = decodeResponse[struct {
		Projects []projectResult `json:"projects"`
	}](t, rr)
	if len(list.Projects) != 1 || list.Projects[0].ID != p.ID {
		t.Fatalf("public list exposed archived projects or lost the active one: %#v", list)
	}
	for _, id := range []string{archived.ID, "missing"} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/projects/"+id, nil, nil), http.StatusNotFound)
	}
	rr = requestJSON(t, handler, http.MethodGet, "/api/admin/projects", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	list = decodeResponse[struct {
		Projects []projectResult `json:"projects"`
	}](t, rr)
	if len(list.Projects) != 2 {
		t.Fatalf("administrator cannot inspect archived projects: %#v", list)
	}
	updated := projectTestInput(p.ID, "EUR", 25000, true)
	updated["name"], updated["url"] = "Renamed project", ""
	rr = projectUpdateFixture(t, a, p, updated)
	expectStatus(t, rr, http.StatusOK)
	p = decodeResponse[projectResult](t, rr)
	if p.Name != "Renamed project" || p.URL != "" || p.Currency != "EUR" || p.TargetMinor != 25000 || p.CreatedAt != replay.CreatedAt {
		t.Fatalf("update lost project properties or rewrote creation time: %#v", p)
	}
	updated["id"] = "another-id"
	expectStatus(t, projectUpdateFixture(t, a, p, updated), http.StatusBadRequest)
	updated["id"], updated["active"] = p.ID, false
	expectStatus(t, projectUpdateFixture(t, a, p, updated), http.StatusOK)
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/projects/"+p.ID, nil, nil), http.StatusNotFound)
	if countRows(t, a, "projects") != 2 || countRows(t, a, "donations") != 0 {
		t.Fatal("editing/archiving changed project identity or added a donation")
	}
}

func TestProjectCRUDRequiresAdministratorSessionAndCSRF(t *testing.T) {
	a := testApp(t)
	donor := donorSessionFixture(t, a)
	routes := a.Routes()
	in := projectTestInput("private-admin", "USD", 10000, true)
	for _, headers := range []map[string]string{nil, donor.headers("project-not-admin-request-001")} {
		expectStatus(t, requestJSON(t, routes, http.MethodGet, "/api/admin/projects", nil, headers), http.StatusUnauthorized)
		expectStatus(t, requestJSON(t, routes, http.MethodPost, "/api/admin/projects", in, headers), http.StatusUnauthorized)
	}
	admin := projectAdminSession(t, a)
	admin["Idempotency-Key"] = "project-admin-authorized-0001"
	for _, csrf := range []string{"", donor.csrf} {
		bad := map[string]string{"Cookie": admin["Cookie"], "X-CSRF-Token": csrf, "Idempotency-Key": admin["Idempotency-Key"]}
		expectStatus(t, requestJSON(t, routes, http.MethodPost, "/api/admin/projects", in, bad), http.StatusForbidden)
	}
	if countRows(t, a, "projects") != 0 {
		t.Fatal("non-administrator or invalid CSRF mutated projects")
	}
	expectStatus(t, requestJSON(t, routes, http.MethodPost, "/api/admin/projects", in, admin), http.StatusCreated)
	expectStatus(t, requestJSON(t, routes, http.MethodGet, "/api/admin/projects", nil, admin), http.StatusOK)
}

func TestProjectInputValidationPreservesMoneyPrecisionAndRuneLimits(t *testing.T) {
	a := testApp(t)
	handler := projectAdminHandler(a)
	for _, test := range []struct {
		name  string
		field string
		value any
	}{
		{"missing slug", "id", ""}, {"unsafe slug", "id", "../private"}, {"slug space", "id", "two words"},
		{"empty name", "name", ""}, {"blank name", "name", "   "}, {"long unicode name", "name", strings.Repeat("界", 81)},
		{"http URL", "url", "http://example.com"}, {"script URL", "url", "javascript:alert(1)"}, {"relative URL", "url", "/project"},
		{"unsupported currency", "currency", "BTC"}, {"empty currency", "currency", ""},
		{"zero goal", "target_minor", int64(0)}, {"negative goal", "target_minor", int64(-1)}, {"fractional goal", "target_minor", 1.5},
		{"overflow goal", "target_minor", json.Number("9223372036854775808")},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := projectTestInput("invalid-input", "USD", 10000, true)
			in[test.field] = test.value
			expectStatus(t, requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, map[string]string{"Idempotency-Key": "project-invalid-input-request-0001"}), http.StatusBadRequest)
			if countRows(t, a, "projects") != 0 {
				t.Fatal("invalid input created a project")
			}
		})
	}
	in := projectTestInput("unicode-boundary", "JPY", math.MaxInt64, true)
	in["name"], in["url"] = strings.Repeat("界", 80), ""
	rr := requestJSON(t, handler, http.MethodPost, "/api/admin/projects", in, map[string]string{"Idempotency-Key": "project-valid-rune-boundary-0001"})
	expectStatus(t, rr, http.StatusCreated)
	p := decodeResponse[projectResult](t, rr)
	if p.Name != strings.Repeat("界", 80) || p.TargetMinor != math.MaxInt64 {
		t.Fatalf("valid input was truncated or lost int64 precision: %#v", p)
	}
}

func TestProjectConcurrentCreateReplaysOneRecord(t *testing.T) {
	a := testApp(t)
	handler := projectAdminHandler(a)
	raw, err := json.Marshal(projectTestInput("concurrent", "USD", 10000, true))
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	results := make(chan *httptest.ResponseRecorder, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/admin/projects", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "project-concurrent-retry-0001")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			results <- rr
		}()
	}
	wg.Wait()
	close(results)
	var first projectResult
	for rr := range results {
		expectStatus(t, rr, http.StatusCreated)
		p := decodeResponse[projectResult](t, rr)
		if first.ID == "" {
			first = p
		}
		if p != first {
			t.Fatalf("concurrent replay changed its stored result: %#v / %#v", first, p)
		}
	}
	if countRows(t, a, "projects") != 1 || countRows(t, a, "donations") != 0 {
		t.Fatal("concurrent project creation duplicated a project or changed the ledger")
	}
}

func TestProjectDonationBindingRejectsInvalidTargetsAndKeepsHistoryPrivate(t *testing.T) {
	a := testApp(t)
	s := testSettings(t, a, customMethod())
	one := projectCreateFixture(t, a, "first", "USD", 10000, true)
	two := projectCreateFixture(t, a, "second", "USD", 20000, true)
	archived := projectCreateFixture(t, a, "closed", "USD", 10000, false)
	owner, other := donorSessionFixture(t, a), donorSessionFixture(t, a)
	for _, invalid := range []struct{ project, currency string }{{"missing", "USD"}, {archived.ID, "USD"}, {one.ID, "XXX"}} {
		in := map[string]any{"amount_minor": 500, "currency": invalid.currency, "method_id": "qr", "project_id": invalid.project, "accepted_terms": true}
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, nil), http.StatusBadRequest)
		expectStatus(t, requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", in, nil), http.StatusBadRequest)
	}
	if countRows(t, a, "donations") != 0 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("invalid project/currency added a ledger record or success notification")
	}
	in := map[string]any{"amount_minor": 1500, "currency": "USD", "method_id": "qr", "project_id": one.ID, "project_name": "Forged project", "name": "Private name", "email": "project-private@example.com", "message": "Private message", "accepted_terms": true}
	key := "project-donation-binding-retry-0001"
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key))
	expectStatus(t, rr, http.StatusOK)
	checkout := decodeResponse[checkoutResponse](t, rr)
	response := decodeResponse[map[string]any](t, rr)
	if response["project_id"] != one.ID || response["project_name"] != one.Name {
		t.Fatalf("checkout trusted a client project name or lost target: %v", response)
	}
	in["project_id"] = two.ID
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key)), http.StatusConflict)
	in["project_id"] = one.ID
	others := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, other.headers(""))
	expectStatus(t, others, http.StatusOK)
	otherCheckout := decodeResponse[checkoutResponse](t, others)
	guest := createCustom(t, a, donationInput{AmountMinor: 700, Currency: "USD"}, "")
	manual := requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", map[string]any{"amount_minor": 300, "currency": "USD", "method_id": "qr", "project_id": two.ID}, map[string]string{"Idempotency-Key": "project-manual-entry-retry-0001"})
	expectStatus(t, manual, http.StatusCreated)
	manualDonation := decodeResponse[Donation](t, manual)
	if manualDonation.ProjectID != two.ID {
		t.Fatal("manual recording did not retain its selected project")
	}
	expectStatus(t, requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", map[string]any{"amount_minor": 300, "currency": "USD", "method_id": "qr", "project_id": one.ID}, map[string]string{"Idempotency-Key": "project-manual-entry-retry-0001"}), http.StatusConflict)
	// A later confirmation cannot move an existing checkout to another project.
	expectStatus(t, confirmCustom(t, a, checkout.ID, map[string]string{"project_id": two.ID, "reference": "statement-project-1"}), http.StatusOK)
	stored, err := a.donation(checkout.ID)
	if err != nil || stored.ProjectID != one.ID || stored.DonorUserID != owner.id || stored.Status != "confirmed" {
		t.Fatalf("confirmation changed immutable donation associations: %#v, %v", stored, err)
	}
	if _, err = a.DB.Exec("UPDATE donations SET project_id=? WHERE id=?", two.ID, checkout.ID); err == nil {
		t.Fatal("the ledger allowed an existing donation to move between projects")
	}
	for _, target := range []string{"/api/donations/" + checkout.ID + "?token=" + checkout.StatusToken, "/api/donor/donations"} {
		headers := map[string]string(nil)
		if target == "/api/donor/donations" {
			headers = owner.headers("")
		}
		rr = requestJSON(t, a.Routes(), http.MethodGet, target, nil, headers)
		expectStatus(t, rr, http.StatusOK)
		if !strings.Contains(rr.Body.String(), `"project_id":"`+one.ID+`"`) || !strings.Contains(rr.Body.String(), `"project_name":"`+one.Name+`"`) {
			t.Fatalf("receipt/history lost its project: %s", rr.Body.String())
		}
		for _, private := range []string{owner.id, other.id, otherCheckout.ID, guest.ID, "project-private@example.com", "Private name", "Private message", checkout.StatusToken, "Forged project"} {
			if strings.Contains(rr.Body.String(), private) {
				t.Fatalf("receipt/history exposed private or unrelated data %q: %s", private, rr.Body.String())
			}
		}
	}
	rr = requestJSON(t, http.HandlerFunc(a.listDonations), http.MethodGet, "/api/admin/donations", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), `"project_id":"`+one.ID+`"`) || !strings.Contains(rr.Body.String(), `"project_name":"`+one.Name+`"`) || !strings.Contains(rr.Body.String(), `"project_id":"`+two.ID+`"`) {
		t.Fatalf("administrator ledger lost project labels: %s", rr.Body.String())
	}
	for _, project := range []projectResult{one, two} {
		changed := projectTestInput(project.ID, "JPY", project.TargetMinor, true)
		expectStatus(t, projectUpdateFixture(t, a, project, changed), http.StatusConflict)
	}
	for _, target := range []string{"/api/projects", "/api/projects/" + one.ID} {
		rr = requestJSON(t, a.Routes(), http.MethodGet, target, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		for _, private := range []string{owner.id, other.id, checkout.ID, checkout.StatusToken, "project-private@example.com", "Private name", "Private message", s.StatsToken, s.Webhook.Secret, s.SMTP.Password, "donor_user_id", "provider_ref"} {
			if strings.Contains(rr.Body.String(), private) {
				t.Fatalf("public projects leaked %q: %s", private, rr.Body.String())
			}
		}
	}
	if oneNow, twoNow := projectReadFixture(t, a, one.ID), projectReadFixture(t, a, two.ID); oneNow.RaisedMinor != 1500 || oneNow.Count != 1 || twoNow.RaisedMinor != 300 || twoNow.Count != 1 {
		t.Fatalf("project totals mixed guests, pending records, or targets: %#v / %#v", oneNow, twoNow)
	}
}

func projectLedgerFixture(t *testing.T, a *App, project string, amount int64, currency, status, paid string) {
	t.Helper()
	tx, err := a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	d := Donation{ID: randomToken(), StatusToken: "project-private-token", AmountMinor: amount, Currency: currency, MethodID: "private-method-id", MethodType: "custom", MethodName: "private-method-name", Name: "project-private-name", Email: "project-private@example.com", Message: "project-private-message", Status: status, Source: "manual", Reference: "project-private-reference", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), PaidAt: paid, DonorUserID: "project-private-account", ProjectID: project}
	if err = insertDonation(tx, d); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectProgressCountsOnlyMatchingConfirmedValidPastPaymentsAndSaturates(t *testing.T) {
	a := testApp(t)
	one := projectCreateFixture(t, a, "statistics", "USD", 1000, true)
	two := projectCreateFixture(t, a, "yen", "JPY", 1000, true)
	paid := time.Now().UTC().Add(-time.Minute)
	for _, fixture := range []struct {
		project, currency, status, date string
		amount                          int64
	}{
		{one.ID, "USD", "confirmed", paid.Format(time.RFC3339Nano), 1250},
		{one.ID, "USD", "confirmed", paid.In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339Nano), 250},
		{one.ID, "USD", "pending", paid.Format(time.RFC3339Nano), 9999},
		{one.ID, "USD", "refunded", paid.Format(time.RFC3339Nano), 8888},
		{one.ID, "USD", "cancelled", paid.Format(time.RFC3339Nano), 7777},
		{one.ID, "USD", "expired", paid.Format(time.RFC3339Nano), 6666},
		{one.ID, "JPY", "confirmed", paid.Format(time.RFC3339Nano), 5555},
		{one.ID, "USD", "confirmed", "not-a-date", 4444},
		{one.ID, "USD", "confirmed", "", 3333},
		{one.ID, "USD", "confirmed", time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), 2222},
		{two.ID, "JPY", "confirmed", paid.Format(time.RFC3339Nano), 500},
		{"", "USD", "confirmed", paid.Format(time.RFC3339Nano), 1111},
	} {
		projectLedgerFixture(t, a, fixture.project, fixture.amount, fixture.currency, fixture.status, fixture.date)
	}
	got, yen := projectReadFixture(t, a, one.ID), projectReadFixture(t, a, two.ID)
	if got.RaisedMinor != 1500 || got.Count != 2 || got.Progress != 100 || yen.RaisedMinor != 500 || yen.Count != 1 || yen.Progress != 50 {
		t.Fatalf("progress included unverified, future, invalid, wrong-currency or other-project money: %#v / %#v", got, yen)
	}
	huge := projectCreateFixture(t, a, "overflow", "USD", math.MaxInt64, true)
	projectLedgerFixture(t, a, huge.ID, math.MaxInt64-10, "USD", "confirmed", paid.Format(time.RFC3339Nano))
	projectLedgerFixture(t, a, huge.ID, 100, "USD", "confirmed", paid.Format(time.RFC3339Nano))
	got = projectReadFixture(t, a, huge.ID)
	if got.RaisedMinor != math.MaxInt64 || got.Count != 2 || got.Progress != 100 {
		t.Fatalf("aggregate overflowed or clamped to an unrelated payment limit: %#v", got)
	}
	precise := projectCreateFixture(t, a, "precise", "USD", math.MaxInt64, true)
	projectLedgerFixture(t, a, precise.ID, 9007199254740993, "USD", "confirmed", paid.Format(time.RFC3339Nano))
	got = projectReadFixture(t, a, precise.ID)
	if got.RaisedMinor != 9007199254740993 || got.TargetMinor != math.MaxInt64 || got.Progress < 0 || got.Progress > 100 || math.IsNaN(got.Progress) || math.IsInf(got.Progress, 0) {
		t.Fatalf("JSON response or progress lost integer precision: %#v", got)
	}
}

func TestProjectProviderSettlementAndRefundPreserveStoredTarget(t *testing.T) {
	a := testApp(t)
	m := stripeMethod("project-card")
	testSettings(t, a, m)
	one := projectCreateFixture(t, a, "provider-first", "USD", 5000, true)
	two := projectCreateFixture(t, a, "provider-second", "USD", 5000, true)
	var original Donation
	a.Payments.Client = &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/checkout/sessions":
			return providerResponse(http.StatusOK, map[string]any{"id": "cs_test_project", "url": "https://checkout.stripe.com/c/pay/project"}), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/checkout/sessions" && req.URL.Query().Get("payment_intent") == "pi_test_project":
			return providerResponse(http.StatusOK, map[string]any{"data": []any{stripePaidObject(original)}, "has_more": false}), nil
		default:
			t.Fatalf("unexpected provider request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", map[string]any{"amount_minor": 1000, "currency": "USD", "method_id": m.ID, "project_id": one.ID, "accepted_terms": true}, map[string]string{"Idempotency-Key": "project-provider-checkout-0001"})
	expectStatus(t, rr, http.StatusOK)
	checkout := decodeResponse[checkoutResponse](t, rr)
	var err error
	original, err = a.donation(checkout.ID)
	if err != nil || original.ProjectID != one.ID {
		t.Fatalf("provider checkout lost selected project: %#v, %v", original, err)
	}
	// Pending ledger entries already lock the accounting currency.
	expectStatus(t, projectUpdateFixture(t, a, one, projectTestInput(one.ID, "JPY", 5000, true)), http.StatusConflict)
	paid := stripePaidObject(original)
	paid["metadata"] = map[string]string{"donation_id": original.ID, "method_id": m.ID, "project_id": two.ID}
	for range 2 {
		expectStatus(t, stripeWebhook(t, a, m.ID, stripeEvent("evt_project_paid", "checkout.session.completed", paid), m.Config["webhook_secret"]), http.StatusOK)
	}
	stored, err := a.donation(original.ID)
	if err != nil || stored.Status != "confirmed" || stored.ProjectID != one.ID || countRows(t, a, "notification_jobs") != 1 {
		t.Fatalf("verified callback moved the project or duplicated settlement: %#v, %v", stored, err)
	}
	if got, untouched := projectReadFixture(t, a, one.ID), projectReadFixture(t, a, two.ID); got.RaisedMinor != 1000 || got.Count != 1 || got.Progress != 20 || untouched.RaisedMinor != 0 || untouched.Count != 0 {
		t.Fatalf("settlement credited another project: %#v / %#v", got, untouched)
	}
	refund := stripeEvent("evt_project_refund", "charge.refunded", map[string]any{"id": "ch_test_project", "amount": 1000, "amount_refunded": 1000, "currency": "usd", "refunded": true, "payment_intent": "pi_test_project"})
	for range 2 {
		expectStatus(t, stripeWebhook(t, a, m.ID, refund, m.Config["webhook_secret"]), http.StatusOK)
	}
	stored, err = a.donation(original.ID)
	if err != nil || stored.Status != "refunded" || stored.ProjectID != one.ID || countRows(t, a, "notification_jobs") != 2 {
		t.Fatalf("refund moved the project or duplicated its event: %#v, %v", stored, err)
	}
	got := projectReadFixture(t, a, one.ID)
	if got.RaisedMinor != 0 || got.Count != 0 || got.Progress != 0 {
		t.Fatalf("refunded payment remained in project progress: %#v", got)
	}
	expectStatus(t, projectUpdateFixture(t, a, one, projectTestInput(one.ID, "JPY", 5000, true)), http.StatusConflict)
	expectStatus(t, stripeWebhook(t, a, m.ID, stripeEvent("evt_project_late_paid", "checkout.session.completed", stripePaidObject(original)), m.Config["webhook_secret"]), http.StatusBadRequest)
	if projectReadFixture(t, a, one.ID).RaisedMinor != 0 {
		t.Fatal("a late success event revived refunded project progress")
	}
}

func TestProjectPublicThanksRequiresAnExplicitBooleanAndDoesNotReuseOldPublicConsent(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	p := projectCreateFixture(t, a, "consent", "USD", 10000, true)
	owner := donorSessionFixture(t, a)
	for _, flag := range []any{nil, "true", "false", 1, 0, []any{true}, map[string]bool{"value": true}} {
		in := map[string]any{"amount_minor": 500, "currency": "USD", "method_id": "qr", "project_id": p.ID, "public_thanks": flag, "accepted_terms": true}
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, nil), http.StatusBadRequest)
		expectStatus(t, requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", in, nil), http.StatusBadRequest)
	}
	if countRows(t, a, "donations") != 0 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("non-boolean consent created a ledger record or notification")
	}
	oldPublic := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", map[string]any{"amount_minor": 500, "currency": "USD", "method_id": "qr", "project_id": p.ID, "public": true, "accepted_terms": true}, nil)
	expectStatus(t, oldPublic, http.StatusOK)
	oldCheckout := decodeResponse[checkoutResponse](t, oldPublic)
	d, err := a.donation(oldCheckout.ID)
	if err != nil || !d.Public || d.PublicThanks {
		t.Fatalf("legacy public visibility implied a new public thanks permission: %#v, %v", d, err)
	}
	in := map[string]any{"amount_minor": 700, "currency": "USD", "method_id": "qr", "project_id": p.ID, "public": false, "public_thanks": true, "accepted_terms": true}
	key := "project-public-thanks-retry-0001"
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key))
	expectStatus(t, rr, http.StatusOK)
	checkout := decodeResponse[checkoutResponse](t, rr)
	if decodeResponse[map[string]any](t, rr)["public_thanks"] != true {
		t.Fatal("checkout omitted the explicit public thanks permission")
	}
	in["public_thanks"] = false
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key)), http.StatusConflict)
	in["public_thanks"] = true
	rr = requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key))
	expectStatus(t, rr, http.StatusOK)
	if decodeResponse[checkoutResponse](t, rr).ID != checkout.ID || countRows(t, a, "donations") != 2 {
		t.Fatal("consent retry duplicated the donation or changed its identity")
	}
	expectStatus(t, confirmCustom(t, a, checkout.ID, map[string]any{"public_thanks": false}), http.StatusOK)
	d, err = a.donation(checkout.ID)
	if err != nil || !d.PublicThanks || d.Public || d.ProjectID != p.ID {
		t.Fatalf("confirmation changed consent or coupled it to legacy visibility: %#v, %v", d, err)
	}
	for _, target := range []string{"/api/donations/" + checkout.ID + "?token=" + checkout.StatusToken, "/api/donor/donations"} {
		headers := map[string]string(nil)
		if target == "/api/donor/donations" {
			headers = owner.headers("")
		}
		rr = requestJSON(t, a.Routes(), http.MethodGet, target, nil, headers)
		expectStatus(t, rr, http.StatusOK)
		if !strings.Contains(rr.Body.String(), `"public_thanks":true`) {
			t.Fatalf("receipt/history omitted its permission: %s", rr.Body.String())
		}
	}
	for _, filter := range []struct {
		value string
		id    string
	}{
		{"true", checkout.ID}, {"false", oldCheckout.ID},
	} {
		rr = requestJSON(t, http.HandlerFunc(a.listDonations), http.MethodGet, "/api/admin/donations?public_thanks="+filter.value, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		ledger := decodeResponse[struct {
			Donations []Donation `json:"donations"`
			Total     int        `json:"total"`
		}](t, rr)
		if ledger.Total != 1 || len(ledger.Donations) != 1 || ledger.Donations[0].ID != filter.id {
			t.Fatalf("administrator permission filter selected the wrong donations: %#v", ledger)
		}
	}
	for _, filter := range []string{"yes", "1", "", "TRUE"} {
		if filter == "" {
			continue // Empty query retains the unfiltered administrator ledger.
		}
		expectStatus(t, requestJSON(t, http.HandlerFunc(a.listDonations), http.MethodGet, "/api/admin/donations?public_thanks="+filter, nil, nil), http.StatusBadRequest)
	}
	manualInput := map[string]any{"amount_minor": 900, "currency": "USD", "method_id": "qr", "project_id": p.ID, "public": false, "public_thanks": true}
	manualKey := map[string]string{"Idempotency-Key": "project-manual-thanks-retry-0001"}
	rr = requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", manualInput, manualKey)
	expectStatus(t, rr, http.StatusCreated)
	manual := decodeResponse[Donation](t, rr)
	if !manual.PublicThanks || manual.Public || manual.ProjectID != p.ID {
		t.Fatalf("manual recording lost the independently selected permission: %#v", manual)
	}
	manualInput["public_thanks"] = false
	expectStatus(t, requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", manualInput, manualKey), http.StatusConflict)
	var payload []byte
	if err = a.DB.QueryRow("SELECT payload FROM notification_jobs WHERE event_id=?", "donation.confirmed_"+checkout.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Donation struct {
			ProjectID    string `json:"project_id"`
			PublicThanks bool   `json:"public_thanks"`
		} `json:"donation"`
	}
	if err = json.Unmarshal(payload, &event); err != nil || event.Donation.ProjectID != p.ID || !event.Donation.PublicThanks {
		t.Fatalf("private notification lost project or public thanks permission: %s, %v", payload, err)
	}
}

func projectDropIndexesForColumn(t *testing.T, a *App, column string) {
	t.Helper()
	rows, err := a.DB.Query("SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='donations' AND sql LIKE ?", "%"+column+"%")
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		indexes = append(indexes, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range indexes {
		if _, err = a.DB.Exec(`DROP INDEX "` + strings.ReplaceAll(name, `"`, `""`) + `"`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectMigrationPreservesSchema3LedgerCredentialsSettingsAndRetries(t *testing.T) {
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
	owner := donorSessionFixture(t, a)
	in := donationInput{AmountMinor: 1234, Currency: "USD", MethodID: "qr", Email: "legacy-project@example.com", Public: true, AcceptedTerms: true}
	key := "legacy-project-guest-retry-0001"
	guest := createCustom(t, a, in, key)
	owned := donorCheckout(t, a, owner, in, "legacy-project-owner-retry-0001")
	expectStatus(t, confirmCustom(t, a, owned.ID, map[string]string{"reference": "legacy-project-statement"}), http.StatusOK)
	before := map[string]Donation{}
	for _, id := range []string{guest.ID, owned.ID} {
		before[id], err = a.donation(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	var settings string
	if err = a.DB.QueryRow("SELECT value FROM settings WHERE key='main'").Scan(&settings); err != nil {
		t.Fatal(err)
	}
	credential := []byte("existing project-era administrator passkey is untouched")
	if _, err = a.DB.Exec("UPDATE auth_state SET password_enabled=0,password_hash=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("INSERT INTO auth_credentials(id,data,active,generation) SELECT 'schema3-admin-key',?,1,generation FROM auth_state WHERE id=1", credential); err != nil {
		t.Fatal(err)
	}
	donorCredential := []byte("existing project-era donor passkey is untouched")
	if _, err = a.DB.Exec("INSERT INTO donor_credentials(id,user_id,data) VALUES('schema3-donor-key',?,?)", owner.id, donorCredential); err != nil {
		t.Fatal(err)
	}
	projectDropIndexesForColumn(t, a, "project_id")
	projectDropIndexesForColumn(t, a, "public_thanks")
	if _, err = a.DB.Exec("DROP TRIGGER IF EXISTS donation_project_immutable; DROP TABLE projects; ALTER TABLE donations DROP COLUMN project_id; ALTER TABLE donations DROP COLUMN public_thanks; PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a = nil
	for range 2 {
		a, err = New(dataDir, "http://localhost:8080", assets)
		if err != nil {
			t.Fatal(err)
		}
		for id, old := range before {
			now, readErr := a.donation(id)
			if readErr != nil || now != old || now.ProjectID != "" || now.PublicThanks {
				t.Fatalf("migration moved or changed a legacy donation: %#v / %#v, %v", old, now, readErr)
			}
		}
		var currentSettings string
		var savedCredential []byte
		var passwordEnabled bool
		var version int
		if err = a.DB.QueryRow("SELECT value FROM settings WHERE key='main'").Scan(&currentSettings); err != nil || currentSettings != settings {
			t.Fatalf("migration rewrote existing settings/payment secrets: %v", err)
		}
		if err = a.DB.QueryRow("SELECT data FROM auth_credentials WHERE id='schema3-admin-key'").Scan(&savedCredential); err != nil || !bytes.Equal(savedCredential, credential) {
			t.Fatalf("migration changed existing administrator credentials: %v", err)
		}
		if err = a.DB.QueryRow("SELECT data FROM donor_credentials WHERE id='schema3-donor-key' AND user_id=?", owner.id).Scan(&savedCredential); err != nil || !bytes.Equal(savedCredential, donorCredential) {
			t.Fatalf("migration changed existing donor credentials or ownership: %v", err)
		}
		if err = a.DB.QueryRow("SELECT password_enabled FROM auth_state WHERE id=1").Scan(&passwordEnabled); err != nil || passwordEnabled {
			t.Fatalf("migration re-enabled password login: %v", err)
		}
		if err = a.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
			t.Fatalf("project schema version = %d, error=%v", version, err)
		}
		if countRows(t, a, "projects") != 0 || countRows(t, a, "donations") != 2 || countRows(t, a, "notification_jobs") != 1 {
			t.Fatal("migration seeded projects, duplicated a donation, or queued an event")
		}
		if retried := createCustom(t, a, in, key); retried != guest {
			t.Fatal("migration invalidated an existing generic checkout fingerprint")
		}
		rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, owner.headers(""))
		expectStatus(t, rr, http.StatusOK)
		history := decodeResponse[donorHistory](t, rr)
		if history.Total != 1 || len(history.Donations) != 1 || history.Donations[0].ID != owned.ID {
			t.Fatal("migration invalidated a donor session or claimed guest donations")
		}
		if err = a.Close(); err != nil {
			t.Fatal(err)
		}
		a = nil
	}
}

func projectSchemaColumnPresent(t *testing.T, a *App, column string) bool {
	t.Helper()
	rows, err := a.DB.Query("PRAGMA table_info(donations)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		found = found || name == column
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func TestProjectMigrationRollsBackAllSchemaChangesOnIndexFailureAndCanRetry(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	checkout := createCustom(t, a, donationInput{AmountMinor: 500, Currency: "USD", Public: true}, "project-migration-rollback-0001")
	before, err := a.donation(checkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectDropIndexesForColumn(t, a, "project_id")
	projectDropIndexesForColumn(t, a, "public_thanks")
	// An existing schema object occupying the required index name makes SQLite
	// fail after the project table and both donation columns have been added.
	// The failed migration must remove all of those partial changes atomically.
	if _, err = a.DB.Exec("DROP TRIGGER IF EXISTS donation_project_immutable; DROP TABLE projects; ALTER TABLE donations DROP COLUMN project_id; ALTER TABLE donations DROP COLUMN public_thanks; CREATE VIEW donation_project_totals AS SELECT id FROM donations; PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err = a.migrateProjects(); err == nil {
		t.Fatal("migration succeeded despite an incompatible required index name")
	}
	var projectTables, version int
	if err = a.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='projects'").Scan(&projectTables); err != nil || projectTables != 0 {
		t.Fatalf("failed migration left a projects table behind: %d, %v", projectTables, err)
	}
	if projectSchemaColumnPresent(t, a, "project_id") || projectSchemaColumnPresent(t, a, "public_thanks") {
		t.Fatal("failed migration left partially added donation columns")
	}
	if err = a.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("failed migration advanced the schema version: %d, %v", version, err)
	}
	if countRows(t, a, "donations") != 1 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("failed migration changed existing ledger/outbox records")
	}
	if _, err = a.DB.Exec("DROP VIEW donation_project_totals"); err != nil {
		t.Fatal(err)
	}
	if err = a.migrateProjects(); err != nil {
		t.Fatal(err)
	}
	after, err := a.donation(checkout.ID)
	if err != nil || after != before || after.ProjectID != "" || after.PublicThanks {
		t.Fatalf("retry changed a legacy donation or inferred a new consent: %#v / %#v, %v", before, after, err)
	}
	if _, err = a.DB.Exec("PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}
	if err = a.migrateProjects(); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 9 {
		t.Fatalf("idempotent migration lowered a newer schema version: %d, %v", version, err)
	}
}
