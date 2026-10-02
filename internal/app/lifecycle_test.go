package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleReceipt struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	CheckoutURL string `json:"checkout_url"`
	ExpiresAt   string `json:"expires_at"`
	CanCancel   bool   `json:"can_cancel"`
}

func lifecycleStripe(t *testing.T, a *App) *atomic.Int64 {
	t.Helper()
	testSettings(t, a, stripeMethod("card"), customMethod())
	calls := new(atomic.Int64)
	a.Payments.Client = &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.stripe.com" || r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			return nil, fmt.Errorf("unexpected provider request: %s %s", r.Method, r.URL)
		}
		id := calls.Add(1)
		return providerResponse(http.StatusOK, map[string]string{
			"id":  fmt.Sprintf("cs_test_lifecycle_%d", id),
			"url": fmt.Sprintf("https://checkout.stripe.com/c/pay/lifecycle-%d", id),
		}), nil
	})}
	return calls
}

func lifecycleHosted(t *testing.T, a *App, user *donorFixture, key string) checkoutResponse {
	t.Helper()
	headers := map[string]string{"Idempotency-Key": key}
	if user != nil {
		headers = user.headers(key)
	}
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", donationInput{
		AmountMinor: 500, Currency: "USD", MethodID: "card", AcceptedTerms: true,
	}, headers)
	expectStatus(t, rr, http.StatusOK)
	checkout := decodeResponse[checkoutResponse](t, rr)
	if checkout.ID == "" || checkout.StatusToken == "" || checkout.Status != "pending" || checkout.CheckoutURL == "" {
		t.Fatalf("invalid hosted checkout: %#v", checkout)
	}
	return checkout
}

func lifecycleStatus(t *testing.T, a *App, checkout checkoutResponse) lifecycleReceipt {
	t.Helper()
	rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/donations/"+checkout.ID+"?token="+checkout.StatusToken, nil, nil)
	expectStatus(t, rr, http.StatusOK)
	return decodeResponse[lifecycleReceipt](t, rr)
}

func lifecycleExpiry(t *testing.T, a *App, id string, expires time.Time) {
	t.Helper()
	if _, err := a.DB.Exec("UPDATE donations SET expires_at=? WHERE id=?", expires.UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

func lifecycleCancel(t *testing.T, a *App, checkout checkoutResponse, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return requestJSON(t, a.Routes(), http.MethodPost, "/api/donations/"+checkout.ID+"/cancel", map[string]string{"status_token": checkout.StatusToken}, headers)
}

func TestDonationLifecycleCancellationRequiresReceiptOriginAndCurrentDonorCSRF(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner, other := donorSessionFixture(t, a), donorSessionFixture(t, a)
	owned := donorCheckout(t, a, owner, donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", AcceptedTerms: true}, "")
	guest := createCustom(t, a, donationInput{AmountMinor: 700, Currency: "USD"}, "")

	for _, test := range []struct {
		name, id, token string
		headers         map[string]string
		want            int
	}{
		{"missing receipt", owned.ID, "", owner.headers(""), http.StatusNotFound},
		{"wrong receipt", owned.ID, guest.StatusToken, owner.headers(""), http.StatusNotFound},
		{"unknown record", randomToken(), owned.StatusToken, owner.headers(""), http.StatusNotFound},
		{"missing origin", owned.ID, owned.StatusToken, map[string]string{"Origin": ""}, http.StatusForbidden},
		{"foreign origin", owned.ID, owned.StatusToken, map[string]string{"Origin": "https://other.example"}, http.StatusForbidden},
		{"cross-site fetch", owned.ID, owned.StatusToken, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"stale session", owned.ID, owned.StatusToken, map[string]string{"Cookie": "donate_donor=unknown"}, http.StatusForbidden},
		{"missing csrf", owned.ID, owned.StatusToken, map[string]string{"Cookie": "donate_donor=" + owner.token}, http.StatusForbidden},
		{"wrong csrf", owned.ID, owned.StatusToken, map[string]string{"Cookie": "donate_donor=" + owner.token, "X-CSRF-Token": other.csrf}, http.StatusForbidden},
		{"different donor", owned.ID, owned.StatusToken, other.headers(""), http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations/"+test.id+"/cancel", map[string]string{"status_token": test.token}, test.headers)
			expectStatus(t, rr, test.want)
			if lifecycleStatus(t, a, owned).Status != "pending" || lifecycleStatus(t, a, guest).Status != "pending" {
				t.Fatal("rejected cancellation changed the ledger")
			}
		})
	}
	// A valid session does not replace the receipt capability. The owner needs
	// both its receipt and its independent donor CSRF token.
	expectStatus(t, lifecycleCancel(t, a, owned, owner.headers("")), http.StatusOK)
	expectStatus(t, lifecycleCancel(t, a, owned, owner.headers("")), http.StatusOK)
	if status := lifecycleStatus(t, a, owned); status.Status != "cancelled" || status.CanCancel || status.CheckoutURL != "" || status.ExpiresAt != "" {
		t.Fatalf("custom cancellation did not remain terminal: %#v", status)
	}
	// The secret receipt also works without an account; an existing login must
	// still pass its CSRF check even when cancelling an original guest record.
	expectStatus(t, lifecycleCancel(t, a, guest, map[string]string{"Cookie": "donate_donor=" + other.token}), http.StatusForbidden)
	expectStatus(t, lifecycleCancel(t, a, guest, nil), http.StatusOK)
	logoutReceipt := donorCheckout(t, a, owner, donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", AcceptedTerms: true}, "")
	expectStatus(t, lifecycleCancel(t, a, logoutReceipt, nil), http.StatusOK)
	if countRows(t, a, "notification_jobs") != 0 || countRows(t, a, "provider_events") != 0 {
		t.Fatal("cancellation generated a payment-success notification or provider event")
	}
}

func TestDonationLifecycleExpiryIsPersistedLazyAndExcludesCustomAndManual(t *testing.T) {
	a := testApp(t)
	_ = lifecycleStripe(t, a)
	hosted := lifecycleHosted(t, a, nil, "")
	custom := createCustom(t, a, donationInput{AmountMinor: 700, Currency: "USD"}, "")
	d, err := a.donation(hosted.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := time.Parse(time.RFC3339Nano, d.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	status := lifecycleStatus(t, a, hosted)
	expires, err := time.Parse(time.RFC3339Nano, status.ExpiresAt)
	if err != nil || !strings.HasSuffix(status.ExpiresAt, "Z") || expires.Sub(created) < 44*time.Minute || expires.Sub(created) > 46*time.Minute || !status.CanCancel {
		t.Fatalf("hosted checkout did not persist a UTC 45-minute expiry: %#v, %v", status, err)
	}
	var stored string
	if err = a.DB.QueryRow("SELECT expires_at FROM donations WHERE id=?", hosted.ID).Scan(&stored); err != nil || stored != status.ExpiresAt {
		t.Fatalf("receipt expiry did not match the database: %q, %v", stored, err)
	}
	if status = lifecycleStatus(t, a, custom); status.ExpiresAt != "" || !status.CanCancel {
		t.Fatalf("custom checkout got a deadline or could not be cancelled: %#v", status)
	}
	manual := requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", donationInput{AmountMinor: 900, Currency: "USD", MethodID: "qr"}, nil)
	expectStatus(t, manual, http.StatusCreated)
	manualDonation := decodeResponse[Donation](t, manual)
	manualDonation, err = a.donation(manualDonation.ID)
	if err != nil {
		t.Fatal(err)
	}
	manualReceipt := checkoutResponse{ID: manualDonation.ID, StatusToken: manualDonation.StatusToken}
	if status = lifecycleStatus(t, a, manualReceipt); status.CanCancel || status.ExpiresAt != "" || status.Status != "confirmed" {
		t.Fatalf("manual record acquired a checkout lifecycle: %#v", status)
	}
	expectStatus(t, lifecycleCancel(t, a, manualReceipt, nil), http.StatusConflict)

	// Reading an invalid receipt must not run a mutating lazy expiry.
	lifecycleExpiry(t, a, hosted.ID, time.Now().Add(-time.Second))
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodGet, "/api/donations/"+hosted.ID+"?token=wrong", nil, nil), http.StatusNotFound)
	d, err = a.donation(hosted.ID)
	if err != nil || d.Status != "pending" {
		t.Fatalf("an invalid capability mutated expiry: %#v, %v", d, err)
	}
	status = lifecycleStatus(t, a, hosted)
	if status.Status != "expired" || status.CanCancel || status.CheckoutURL != "" {
		t.Fatalf("lazy expiry returned an active checkout: %#v", status)
	}
	expectStatus(t, lifecycleCancel(t, a, hosted, nil), http.StatusOK)
	if lifecycleStatus(t, a, hosted).Status != "expired" {
		t.Fatal("cancellation rewrote an expired receipt")
	}

	future := lifecycleHosted(t, a, nil, "")
	boundary := time.Now().UTC().Add(time.Hour)
	lifecycleExpiry(t, a, future.ID, boundary)
	if _, err = a.DB.Exec("UPDATE donations SET created_at=? WHERE id=?", time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano), custom.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.expireDonations(context.Background(), boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err = a.donation(future.ID)
	if err != nil || d.Status != "pending" {
		t.Fatalf("worker expired a future checkout early: %#v, %v", d, err)
	}
	if err = a.expireDonations(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	d, err = a.donation(future.ID)
	if err != nil || d.Status != "expired" {
		t.Fatalf("worker did not expire at the exact deadline: %#v, %v", d, err)
	}
	if lifecycleStatus(t, a, custom).Status != "pending" || lifecycleStatus(t, a, manualReceipt).Status != "confirmed" || countRows(t, a, "notification_jobs") != 1 {
		t.Fatal("expiry changed an offline/manual donation or queued a payment notification")
	}
}

func TestDonationLifecycleTerminalRetryAndLateSignedPaymentPreserveOwnership(t *testing.T) {
	for _, terminal := range []string{"cancelled", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			a := testApp(t)
			calls := lifecycleStripe(t, a)
			owner := donorSessionFixture(t, a)
			key := "lifecycle-terminal-retry-" + terminal
			checkout := lifecycleHosted(t, a, &owner, key)
			if terminal == "cancelled" {
				expectStatus(t, lifecycleCancel(t, a, checkout, owner.headers("")), http.StatusOK)
			} else {
				lifecycleExpiry(t, a, checkout.ID, time.Now().Add(-time.Second))
				if err := a.expireDonations(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			in := donationInput{AmountMinor: 500, Currency: "USD", MethodID: "card", AcceptedTerms: true}
			retry := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, owner.headers(key))
			expectStatus(t, retry, http.StatusOK)
			replayed := decodeResponse[checkoutResponse](t, retry)
			if replayed.ID != checkout.ID || replayed.StatusToken != checkout.StatusToken || replayed.Status != terminal || replayed.CheckoutURL != "" || calls.Load() != 1 || countRows(t, a, "donations") != 1 {
				t.Fatalf("terminal retry restarted checkout or lost its receipt: %#v, calls=%d", replayed, calls.Load())
			}
			before, err := a.donation(checkout.ID)
			if err != nil {
				t.Fatal(err)
			}
			m := stripeMethod("card")
			wrongAmount := stripePaidObject(before)
			wrongAmount["amount_total"] = before.AmountMinor + 1
			expectStatus(t, stripeWebhook(t, a, m.ID, stripeEvent("evt_lifecycle_wrong", "checkout.session.completed", wrongAmount), m.Config["webhook_secret"]), http.StatusBadRequest)
			if countRows(t, a, "notification_jobs") != 0 || countRows(t, a, "provider_events") != 0 {
				t.Fatal("an invalid late payment mutated ledger/outbox")
			}
			paid := stripeEvent("evt_lifecycle_paid", "checkout.session.completed", stripePaidObject(before))
			for range 2 {
				expectStatus(t, stripeWebhook(t, a, m.ID, paid, m.Config["webhook_secret"]), http.StatusOK)
			}
			expectStatus(t, stripeWebhook(t, a, m.ID, stripeEvent("evt_lifecycle_paid_again", "checkout.session.async_payment_succeeded", stripePaidObject(before)), m.Config["webhook_secret"]), http.StatusOK)
			after, err := a.donation(checkout.ID)
			if err != nil || after.Status != "confirmed" || after.DonorUserID != owner.id || after.PaidAt == "" || after.ProviderRef != before.ProviderRef || countRows(t, a, "provider_events") != 2 || countRows(t, a, "notification_jobs") != 1 {
				t.Fatalf("late settlement lost identity or was not exactly once: %#v, %v", after, err)
			}
			expectStatus(t, lifecycleCancel(t, a, checkout, owner.headers("")), http.StatusConflict)
			if err = a.expireDonations(context.Background(), time.Now().Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if status := lifecycleStatus(t, a, checkout); status.Status != "confirmed" || status.CanCancel || status.CheckoutURL != "" {
				t.Fatalf("expiry or cancellation overwrote actual payment: %#v", status)
			}
			stats, err := a.stats("USD")
			if err != nil || stats.Count != 1 || stats.TotalMinor != 500 {
				t.Fatalf("late payment was not counted once: %#v, %v", stats, err)
			}
			history := requestJSON(t, a.Routes(), http.MethodGet, "/api/donor/donations", nil, owner.headers(""))
			expectStatus(t, history, http.StatusOK)
			owned := decodeResponse[donorHistory](t, history)
			if owned.Total != 1 || len(owned.Donations) != 1 || owned.Donations[0].ID != checkout.ID || owned.Donations[0].Status != "confirmed" {
				t.Fatalf("late payment disappeared from the owner's history: %#v", owned)
			}
		})
	}
}

func TestDonationLifecycleInFlightProviderResultCannotReviveTerminalCheckout(t *testing.T) {
	for _, terminal := range []string{"cancelled", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			a := testApp(t)
			testSettings(t, a, stripeMethod("card"))
			started, release := make(chan struct{}), make(chan struct{})
			a.Payments.Client = &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return providerResponse(http.StatusOK, map[string]string{"id": "cs_test_inflight", "url": "https://checkout.stripe.com/c/pay/inflight"}), nil
			})}
			key := "lifecycle-inflight-checkout-" + terminal
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				raw := `{"amount_minor":500,"currency":"USD","method_id":"card","accepted_terms":true}`
				r := httptest.NewRequest(http.MethodPost, "/api/donations", strings.NewReader(raw))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", a.PublicURL)
				r.Header.Set("Idempotency-Key", key)
				rr := httptest.NewRecorder()
				a.Routes().ServeHTTP(rr, r)
				result <- rr
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("provider checkout did not start")
			}
			var checkout checkoutResponse
			if err := a.DB.QueryRow("SELECT id,status_token FROM donations WHERE checkout_key=?", key).Scan(&checkout.ID, &checkout.StatusToken); err != nil {
				close(release)
				t.Fatal(err)
			}
			if terminal == "cancelled" {
				rr := lifecycleCancel(t, a, checkout, nil)
				if rr.Code != http.StatusOK {
					close(release)
					t.Fatalf("in-flight cancellation failed: %d %s", rr.Code, rr.Body.String())
				}
			} else {
				lifecycleExpiry(t, a, checkout.ID, time.Now().Add(-time.Second))
				if err := a.expireDonations(context.Background(), time.Now()); err != nil {
					close(release)
					t.Fatal(err)
				}
			}
			close(release)
			select {
			case rr := <-result:
				expectStatus(t, rr, http.StatusOK)
				returned := decodeResponse[checkoutResponse](t, rr)
				if returned.ID != checkout.ID || returned.Status != terminal || returned.CheckoutURL != "" {
					t.Fatalf("in-flight provider result revived a terminal receipt: %#v", returned)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("provider checkout did not finish")
			}
			if status := lifecycleStatus(t, a, checkout); status.Status != terminal || status.CanCancel || status.CheckoutURL != "" {
				t.Fatalf("in-flight provider result changed persistent terminal state: %#v", status)
			}
		})
	}
}

func TestDonationLifecycleCancelledCustomCanBeConfirmedAfterOfflineReconciliation(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	owner := donorSessionFixture(t, a)
	checkout := donorCheckout(t, a, owner, donationInput{AmountMinor: 500, Currency: "USD", MethodID: "qr", AcceptedTerms: true}, "")
	expectStatus(t, lifecycleCancel(t, a, checkout, owner.headers("")), http.StatusOK)
	for range 2 {
		expectStatus(t, confirmCustom(t, a, checkout.ID, map[string]string{"reference": "offline-statement-late"}), http.StatusOK)
	}
	d, err := a.donation(checkout.ID)
	if err != nil || d.Status != "confirmed" || d.DonorUserID != owner.id || d.Reference != "offline-statement-late" || countRows(t, a, "notification_jobs") != 1 {
		t.Fatalf("offline reconciliation lost actual payment or duplicated the event: %#v, %v", d, err)
	}
	expectStatus(t, lifecycleCancel(t, a, checkout, owner.headers("")), http.StatusConflict)
	if _, err = a.DB.Exec("UPDATE donations SET status='refunded' WHERE id=?", checkout.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, lifecycleCancel(t, a, checkout, owner.headers("")), http.StatusConflict)
	if lifecycleStatus(t, a, checkout).Status != "refunded" || countRows(t, a, "notification_jobs") != 1 {
		t.Fatal("cancellation changed a refunded payment or queued another notification")
	}
}

func TestDonationLifecycleConcurrentCancelExpiryAndPaidEventCannotLosePayment(t *testing.T) {
	a := testApp(t)
	_ = lifecycleStripe(t, a)
	owner := donorSessionFixture(t, a)
	m := stripeMethod("card")
	for iteration := range 4 {
		checkout := lifecycleHosted(t, a, &owner, "")
		d, err := a.donation(checkout.ID)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		cancelled := make(chan *httptest.ResponseRecorder, 1)
		settled := make(chan *httptest.ResponseRecorder, 1)
		expired := make(chan error, 1)
		go func() {
			<-start
			cancelled <- lifecycleCancel(t, a, checkout, owner.headers(""))
		}()
		go func() {
			<-start
			settled <- stripeWebhook(t, a, m.ID, stripeEvent(fmt.Sprintf("evt_lifecycle_race_%d", iteration), "checkout.session.completed", stripePaidObject(d)), m.Config["webhook_secret"])
		}()
		go func() {
			<-start
			expired <- a.expireDonations(context.Background(), time.Now().Add(time.Hour))
		}()
		close(start)
		cancel := <-cancelled
		if cancel.Code != http.StatusOK && cancel.Code != http.StatusConflict {
			t.Fatalf("racing cancellation = %d, body=%s", cancel.Code, cancel.Body.String())
		}
		expectStatus(t, <-settled, http.StatusOK)
		if err = <-expired; err != nil {
			t.Fatal(err)
		}
		after, err := a.donation(checkout.ID)
		if err != nil || after.Status != "confirmed" || after.DonorUserID != owner.id || after.PaidAt == "" {
			t.Fatalf("racing cancellation/expiry overwrote actual payment: %#v, %v", after, err)
		}
		if countRows(t, a, "notification_jobs") != iteration+1 || countRows(t, a, "provider_events") != iteration+1 {
			t.Fatal("payment race duplicated or lost its settlement event")
		}
	}
}

func TestDonationLifecycleExpiryWorkerStartupAndShutdown(t *testing.T) {
	a := testApp(t)
	calls := lifecycleStripe(t, a)
	checkout := lifecycleHosted(t, a, nil, "")
	lifecycleExpiry(t, a, checkout.ID, time.Now().Add(-time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runCheckoutExpiry(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("expiry worker did not stop after context cancellation")
		}
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		d, err := a.donation(checkout.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == "expired" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("worker waited for its recurring tick instead of expiring persisted deadlines at startup")
		case <-poll.C:
		}
	}
	if calls.Load() != 1 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("expiry worker called the payment provider or queued a success notification")
	}
}

func TestDonationLifecycleMigrationBackfillsOnlyLegacyPendingHostedAndPreservesAdministrator(t *testing.T) {
	a := testApp(t)
	_ = lifecycleStripe(t, a)
	hosted := lifecycleHosted(t, a, nil, "legacy-lifecycle-checkout-001")
	paid := lifecycleHosted(t, a, nil, "legacy-lifecycle-checkout-002")
	custom := createCustom(t, a, donationInput{AmountMinor: 700, Currency: "USD"}, "")
	created := time.Date(2026, 10, 1, 10, 20, 30, 123456789, time.UTC)
	if _, err := a.DB.Exec("UPDATE donations SET created_at=?", created.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec("UPDATE donations SET status='confirmed',paid_at=? WHERE id=?", created.Format(time.RFC3339Nano), paid.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec("UPDATE auth_state SET password_enabled=0,password_hash=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	savedKey := []byte("existing administrator credential survives checkout migration")
	if _, err := a.DB.Exec("INSERT INTO auth_credentials(id,data,active,generation) SELECT ?,?,1,generation FROM auth_state WHERE id=1", "legacy-lifecycle-admin-key", savedKey); err != nil {
		t.Fatal(err)
	}
	var originalSettings string
	if err := a.DB.QueryRow("SELECT value FROM settings WHERE key='main'").Scan(&originalSettings); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]Donation)
	for _, id := range []string{hosted.ID, paid.ID, custom.ID} {
		d, err := a.donation(id)
		if err != nil {
			t.Fatal(err)
		}
		d.ExpiresAt = ""
		before[id] = d
	}
	// Reproduce a database from the release before checkout expiry existed.
	rows, err := a.DB.Query("SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='donations' AND sql LIKE '%expires_at%'")
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
	if _, err = a.DB.Exec("ALTER TABLE donations DROP COLUMN expires_at; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = a.migrateCheckoutExpiry(); err != nil {
			t.Fatal(err)
		}
	}
	for id, old := range before {
		after, err := a.donation(id)
		if err != nil {
			t.Fatal(err)
		}
		expires := after.ExpiresAt
		after.ExpiresAt = ""
		if after != old {
			t.Fatalf("expiry migration changed the existing ledger: %#v / %#v", old, after)
		}
		want := ""
		if id == hosted.ID {
			want = created.Add(45 * time.Minute).Format(time.RFC3339Nano)
		}
		if expires != want {
			t.Fatalf("migration deadline for %s = %q, want %q", id, expires, want)
		}
	}
	var savedCredential []byte
	var enabled bool
	var settings string
	var version int
	if err = a.DB.QueryRow("SELECT data FROM auth_credentials WHERE id='legacy-lifecycle-admin-key'").Scan(&savedCredential); err != nil || !bytes.Equal(savedCredential, savedKey) {
		t.Fatalf("checkout migration changed an administrator credential: %v", err)
	}
	if err = a.DB.QueryRow("SELECT password_enabled FROM auth_state WHERE id=1").Scan(&enabled); err != nil || enabled {
		t.Fatalf("checkout migration re-enabled password login: %v", err)
	}
	if err = a.DB.QueryRow("SELECT value FROM settings WHERE key='main'").Scan(&settings); err != nil || settings != originalSettings {
		t.Fatalf("checkout migration changed settings or payment credentials: %v", err)
	}
	if err = a.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("checkout migration version = %d, error=%v", version, err)
	}
	if countRows(t, a, "donations") != 3 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatal("migration added a donation or queued a payment notification")
	}
}
