package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

type cryptoTransport func(*http.Request) (*http.Response, error)

func (f cryptoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cryptoFixture struct {
	a                   *App
	recipient, contract string
	chainID             int64
	mu                  sync.Mutex
	amounts             map[string]int64
	final               bool
	unavailable         bool
	calls               int
}

func newCryptoFixture(t *testing.T) *cryptoFixture {
	t.Helper()
	a := testApp(t)
	s, e := a.settings()
	if e != nil {
		t.Fatal(e)
	}
	s.Crypto.Networks = s.Crypto.Networks[:1]
	s.Crypto.Networks[0].Enabled = true
	s.Crypto.Networks[0].WSURL = ""
	s.Crypto.Addresses["evm"] = "0x" + strings.Repeat("1", 40)
	s.Methods = []payments.Method{{ID: "crypto", Type: "crypto", Name: "Crypto", Enabled: true}}
	s.Webhook = notify.WebhookConfig{Enabled: true, URL: "https://example.com/payment-events", Secret: "test-outgoing-secret"}
	s.Crypto.EventSecret = strings.Repeat("s", 32)
	if e = a.saveSettings(s); e != nil {
		t.Fatal(e)
	}
	f := &cryptoFixture{a: a, recipient: s.Crypto.Addresses["evm"], contract: s.Crypto.Networks[0].Tokens[0].Contract, chainID: s.Crypto.Networks[0].ChainID, amounts: map[string]int64{}, final: true}
	a.Chain.Client = &http.Client{Transport: cryptoTransport(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if f.unavailable {
			return nil, fmt.Errorf("private-node-credential-do-not-leak")
		}
		var in struct {
			Method string
			Params []json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&in)
		var result any
		switch in.Method {
		case "eth_chainId":
			result = fmt.Sprintf("0x%x", f.chainID)
		case "eth_getTransactionReceipt":
			var id string
			json.Unmarshal(in.Params[0], &id)
			amount, ok := f.amounts[id]
			if ok {
				result = map[string]any{"transactionHash": id, "status": "0x1", "blockNumber": "0x64", "blockHash": "canonical", "logs": []any{map[string]any{"address": f.contract, "topics": []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x" + strings.Repeat("0", 64), "0x" + strings.Repeat("0", 24) + f.recipient[2:]}, "data": fmt.Sprintf("0x%064x", amount)}}}
			}
		case "eth_blockNumber":
			result = "0x100"
		case "eth_getBlockByNumber":
			var tag string
			json.Unmarshal(in.Params[0], &tag)
			number := "0x100"
			if tag == "finalized" && !f.final {
				number = "0x63"
			}
			result = map[string]string{"number": number, "hash": "canonical", "timestamp": fmt.Sprintf("0x%x", time.Now().Unix())}
		default:
			t.Errorf("unexpected RPC %s", in.Method)
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	return f
}
func (f *cryptoFixture) create(t *testing.T, key, network string) checkoutResponse {
	t.Helper()
	rr := requestJSON(t, f.a.Routes(), "POST", "/api/donations", donationInput{MethodID: "crypto", CryptoNetwork: network, CryptoAsset: "USDT", Currency: "USD", AmountMinor: 1000, AcceptedTerms: true}, map[string]string{"Idempotency-Key": key})
	expectStatus(t, rr, 200)
	return decodeResponse[checkoutResponse](t, rr)
}
func (f *cryptoFixture) submit(t *testing.T, d checkoutResponse, hash string) map[string]any {
	t.Helper()
	p, e := readCrypto(f.a.DB, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	rr := requestJSON(t, f.a.Routes(), "POST", "/api/donations/"+d.ID+"/transactions", map[string]string{"status_token": d.StatusToken, "network": p.Quote.Network.ID, "tx_id": hash}, nil)
	expectStatus(t, rr, 200)
	return decodeResponse[map[string]any](t, rr)
}
func cryptoHash(s string) string { return "0x" + strings.Repeat(s, 64) }
func TestCryptoTolerancePartialAndOverpaid(t *testing.T) {
	for _, tc := range []struct {
		name, state  string
		units, minor int64
	}{{"boundary", "paid", 9900000, 990}, {"below", "partial", 9899999, 1000}, {"overpaid", "overpaid", 10500000, 1050}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCryptoFixture(t)
			d := f.create(t, "crypto-tolerance-test-"+tc.name, "plasma")
			f.amounts[cryptoHash("a")] = tc.units
			r := f.submit(t, d, cryptoHash("a"))
			v := r["crypto"].(map[string]any)
			if v["state"] != tc.state || v["received_units"] != strconv.FormatInt(tc.units, 10) || v["expected"] != "10" {
				t.Fatal(r)
			}
			row, _ := f.a.donation(d.ID)
			if row.AmountMinor != tc.minor {
				t.Fatalf("ledger records expected instead of actual: %+v", row)
			}
			jobs := countRows(t, f.a, "notification_jobs")
			f.submit(t, d, cryptoHash("a"))
			if countRows(t, f.a, "notification_jobs") != jobs {
				t.Fatal("duplicate settlement event")
			}
			if tc.state == "partial" {
				f.amounts[cryptoHash("b")] = 100001
				r = f.submit(t, d, cryptoHash("b"))
				if r["crypto"].(map[string]any)["received"] != "10" || r["status"] != "confirmed" {
					t.Fatal(r)
				}
			}
			var payload string
			if e := f.a.DB.QueryRow("SELECT payload FROM notification_jobs WHERE json_extract(payload,'$.type')='payment." + strings.ReplaceAll(tc.state, "overpaid", "paid") + "' ORDER BY created_at LIMIT 1").Scan(&payload); e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(payload, "expected_units") || !strings.Contains(payload, "received_units") {
				t.Fatal(payload)
			}
		})
	}
}
func TestCryptoReplayProtectionUsesChainIdentity(t *testing.T) {
	f := newCryptoFixture(t)
	s, _ := f.a.settings()
	alias := s.Crypto.Networks[0]
	alias.ID = "alias"
	s.Crypto.Networks = append(s.Crypto.Networks, alias)
	if e := f.a.saveSettings(s); e != nil {
		t.Fatal(e)
	}
	first := f.create(t, "crypto-first-replay-order", "plasma")
	second := f.create(t, "crypto-second-replay-order", "alias")
	f.amounts[cryptoHash("a")] = 10000000
	f.submit(t, first, cryptoHash("a"))
	r := f.submit(t, second, cryptoHash("a"))
	v := r["crypto"].(map[string]any)
	if v["reason"] != "already_used" || r["status"] == "confirmed" || v["received"] != "0" {
		t.Fatal(r)
	}
}
func TestCryptoSnapshotRetirementAndAuthorization(t *testing.T) {
	f := newCryptoFixture(t)
	d := f.create(t, "crypto-snapshot-order-test", "plasma")
	s, _ := f.a.settings()
	s.Crypto.Networks[0].Enabled = false
	s.Crypto.Addresses["evm"] = "0x" + strings.Repeat("2", 40)
	s.Methods[0].Enabled = false
	if e := f.a.saveSettings(s); e != nil {
		t.Fatal(e)
	}
	p, _ := readCrypto(f.a.DB, d.ID)
	if p.Quote.Recipient != f.recipient {
		t.Fatal("saved address changed")
	}
	for _, path := range []string{"/api/donations/" + d.ID, "/api/donations/" + d.ID + "/qr", "/api/donations/" + d.ID + "/events"} {
		rr := requestJSON(t, f.a.Routes(), "GET", path+"?token=wrong", nil, nil)
		expectStatus(t, rr, 404)
	}
	f.amounts[cryptoHash("a")] = 10000000
	r := f.submit(t, d, cryptoHash("a"))
	if r["status"] != "confirmed" {
		t.Fatal("retired network broke pending quote", r)
	}
	rr := requestJSON(t, f.a.Routes(), "GET", "/api/donations/"+d.ID+"?token="+d.StatusToken, nil, nil)
	expectStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), `"rpc_url":`) || strings.Contains(rr.Body.String(), "event_secret") {
		t.Fatal("secret leaked")
	}
}
func TestCryptoKnownHashConfirmationRecoveryAndDeadline(t *testing.T) {
	f := newCryptoFixture(t)
	f.final = false
	d := f.create(t, "crypto-confirmation-order", "plasma")
	f.amounts[cryptoHash("a")] = 10000000
	r := f.submit(t, d, cryptoHash("a"))
	if r["crypto"].(map[string]any)["state"] != "confirming" || r["status"] == "confirmed" {
		t.Fatal(r)
	}
	f.unavailable = true
	r = f.submit(t, d, cryptoHash("a"))
	v := r["crypto"].(map[string]any)
	if v["reason"] != "rpc_unavailable" || v["observed"] != "10" || v["received"] != "0" {
		t.Fatal(r)
	}
	f.unavailable = false
	f.final = true
	rr := requestJSON(t, f.a.Routes(), "POST", "/api/donations/"+d.ID+"/recheck", map[string]string{"status_token": d.StatusToken}, nil)
	expectStatus(t, rr, 200)
	if decodeResponse[map[string]any](t, rr)["status"] != "confirmed" {
		t.Fatal(rr.Body.String())
	}
	next := f.create(t, "crypto-deadline-order-test", "plasma")
	f.a.DB.Exec("UPDATE crypto_payments SET deadline=? WHERE donation_id=?", time.Now().Add(-time.Second).Unix(), next.ID)
	rr = requestJSON(t, f.a.Routes(), "POST", "/api/donations/"+next.ID+"/transactions", map[string]string{"status_token": next.StatusToken, "network": "plasma", "tx_id": cryptoHash("b")}, nil)
	expectStatus(t, rr, 409)
}
func TestCryptoSignedEventsCannotSettleAndAreIdempotent(t *testing.T) {
	f := newCryptoFixture(t)
	d := f.create(t, "crypto-incoming-hook-order", "plasma")
	f.submit(t, d, cryptoHash("a"))
	body := []byte(`{"event_id":"event-test","network":"plasma","tx_id":"` + cryptoHash("a") + `","status":"paid","amount":10000}`)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 32)))
	mac.Write([]byte(stamp + "."))
	mac.Write(body)
	send := func(signature string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/crypto/events", bytes.NewReader(body))
		req.Header.Set("X-Donate-Timestamp", stamp)
		req.Header.Set("X-Donate-Signature", signature)
		rr := httptest.NewRecorder()
		f.a.Routes().ServeHTTP(rr, req)
		return rr
	}
	expectStatus(t, send("bad"), 401)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	expectStatus(t, send(sig), 200)
	expectStatus(t, send(sig), 200)
	if countRows(t, f.a, "crypto_hook_events") != 1 {
		t.Fatal("event replay not deduplicated")
	}
	row, _ := f.a.donation(d.ID)
	if row.Status == "confirmed" {
		t.Fatal("untrusted status settled a payment")
	}
	if e := f.a.verifyCrypto(context.Background(), d.ID, cryptoHash("a")); e != nil {
		t.Fatal(e)
	}
	row, _ = f.a.donation(d.ID)
	if row.Status == "confirmed" {
		t.Fatal("missing hash settled")
	}
}

func TestCryptoSSEDeliversAuthoritativeSnapshotsAndReconnects(t *testing.T) {
	f := newCryptoFixture(t)
	d := f.create(t, "crypto-sse-live-order-test", "plasma")
	server := httptest.NewServer(f.a.Routes())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/donations/"+d.ID+"/events?token="+d.StatusToken, nil)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Status)
	}
	reader := bufio.NewReader(resp.Body)
	snapshot := func() map[string]any {
		t.Helper()
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				t.Fatal(e)
			}
			if strings.HasPrefix(line, "data: ") {
				var result map[string]any
				if e = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &result); e != nil {
					t.Fatal(e)
				}
				return result
			}
		}
	}
	if snapshot()["crypto"].(map[string]any)["state"] != "open" {
		t.Fatal("first stream state")
	}
	f.amounts[cryptoHash("a")] = 10000000
	f.submit(t, d, cryptoHash("a"))
	if snapshot()["status"] != "confirmed" {
		t.Fatal("settlement was not pushed")
	}
	resp.Body.Close()
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	reader = bufio.NewReader(resp.Body)
	if snapshot()["status"] != "confirmed" {
		t.Fatal("reconnection lost paid state")
	}
}
