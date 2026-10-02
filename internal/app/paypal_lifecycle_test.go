package app

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

func TestPayPalReturnNeverStartsNewCaptureAfterCancellationOrExpiry(t *testing.T) {
	for _, state := range []string{"cancelled", "expired", "pending-expired"} {
		t.Run(state, func(t *testing.T) {
			a := testApp(t)
			testSettings(t, a, payments.Method{ID: "paypal", Type: "paypal", Name: "PayPal", Enabled: true, Config: map[string]string{"environment": "sandbox", "client_id": "test-client", "client_secret": "test-secret", "webhook_id": "test-webhook"}})
			a.Payments.Client = &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
				t.Errorf("terminal PayPal return attempted a provider request: %s %s", r.Method, r.URL)
				return nil, fmt.Errorf("unexpected provider request")
			})}
			d := Donation{ID: randomToken(), StatusToken: randomToken(), AmountMinor: 500, Currency: "USD", MethodID: "paypal", MethodType: "paypal", MethodName: "PayPal", Source: "checkout", Status: state, ProviderRef: "ORDER-LIFECYCLE", CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), ExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}
			if state == "pending-expired" {
				d.Status = "pending"
			}
			tx, err := a.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = insertDonation(tx, d); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			rr := requestJSON(t, a.Routes(), http.MethodGet, "/api/paypal/return?donation="+d.ID+"&status_token="+d.StatusToken+"&token="+d.ProviderRef, nil, nil)
			expectStatus(t, rr, http.StatusSeeOther)
			after, err := a.donation(d.ID)
			want := state
			if state == "pending-expired" {
				want = "expired"
			}
			if err != nil || after.Status != want || after.PaidAt != "" || after.ProviderRef != d.ProviderRef || countRows(t, a, "notification_jobs") != 0 {
				t.Fatalf("terminal PayPal return captured or changed a payment: %#v, %v", after, err)
			}
		})
	}
}
