package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testService(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func queueEvent(t *testing.T, s *Service, event Event, cfg Config) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.Queue(tx, event, cfg); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func testEvent(id string) Event {
	return Event{
		ID: id, Type: "donation.completed", CreatedAt: "2026-10-03T02:34:56Z",
		Donation: map[string]any{"donor_name": "Alice", "amount_minor": 1200, "currency": "USD", "payment_method": "waffo"},
	}
}

func TestTransactionalQueueRollbackIdempotencyAndPersistence(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	path := filepath.Join(t.TempDir(), "outbox.sqlite")
	db := openTestDB(t, path)
	s := testService(t, db)
	cfg := Config{Webhook: WebhookConfig{Enabled: true, URL: "http://127.0.0.1:12345/events", Secret: "original-secret"}}
	if _, err := db.Exec(`CREATE TABLE test_donations (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO test_donations VALUES ('rolled-back')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue(tx, testEvent("rolled-back"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.List(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("rollback left outbox records: jobs=%v err=%v", jobs, err)
	}
	var donations int
	if err := db.QueryRow(`SELECT count(*) FROM test_donations`).Scan(&donations); err != nil || donations != 0 {
		t.Fatalf("rollback left donation records: count=%d err=%v", donations, err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO test_donations VALUES ('committed')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue(tx, testEvent("committed"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cfg.Webhook.Secret = "replacement-secret"
	queueEvent(t, s, testEvent("committed"), cfg)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s = testService(t, openTestDB(t, path))
	jobs, err = s.List(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("committed event did not survive once: jobs=%v err=%v", jobs, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM test_donations`).Scan(&donations); err != nil || donations != 1 {
		t.Fatalf("committed donation did not survive: count=%d err=%v", donations, err)
	}
	var saved []byte
	if err := s.db.QueryRow(`SELECT config FROM notification_jobs WHERE id = ?`, jobs[0].ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var snapshot WebhookConfig
	if err := json.Unmarshal(saved, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Secret != "original-secret" {
		t.Fatal("duplicate queueing replaced the original configuration snapshot")
	}
	listed, _ := json.Marshal(jobs)
	if strings.Contains(string(listed), "secret") || strings.Contains(string(listed), "Alice") || strings.Contains(string(listed), "config") {
		t.Fatal("job listing disclosed payload or credentials")
	}
}

func TestWebhookSignatureExactBodyAndStableDeliveryIDAcrossRetry(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	// An environment proxy must not receive either the webhook or credentials.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook unexpectedly used an environment proxy")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	var requests atomic.Int32
	var firstDeliveryID string
	var firstBody []byte
	secret := "hmac-snapshot-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if !hmac.Equal([]byte(r.Header.Get("X-Donate-Signature")), []byte("sha256="+hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("signature did not authenticate the exact received bytes")
		}
		if r.Header.Get("X-Donate-Event") != "donation.completed" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("webhook event headers were missing")
		}
		if requests.Add(1) == 1 {
			firstDeliveryID = r.Header.Get("X-Donate-Delivery")
			firstBody = append([]byte(nil), body...)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, secret+strings.Repeat("x", 100000))
			return
		}
		if firstDeliveryID == "" || firstDeliveryID != r.Header.Get("X-Donate-Delivery") || string(firstBody) != string(body) {
			t.Error("retry changed the delivery identity or body")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	s := testService(t, openTestDB(t, filepath.Join(t.TempDir(), "outbox.sqlite")))
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	queueEvent(t, s, testEvent("webhook-signature"), Config{Webhook: WebhookConfig{Enabled: true, URL: server.URL, Secret: secret}})
	if found, err := s.deliverNext(context.Background()); err != nil || !found {
		t.Fatalf("first delivery: found=%v err=%v", found, err)
	}
	jobs, _ := s.List(context.Background())
	if jobs[0].Status != "pending" || jobs[0].Attempts != 1 || jobs[0].LastError != "webhook responded HTTP 503" {
		t.Fatalf("unexpected retry state: %+v", jobs[0])
	}
	if found, err := s.deliverNext(context.Background()); err != nil || found {
		t.Fatalf("delivery retried before it was due: found=%v err=%v", found, err)
	}
	now = now.Add(30 * time.Second)
	if found, err := s.deliverNext(context.Background()); err != nil || !found {
		t.Fatalf("second delivery: found=%v err=%v", found, err)
	}
	jobs, _ = s.List(context.Background())
	if jobs[0].Status != "delivered" || jobs[0].Attempts != 2 || jobs[0].LastError != "" {
		t.Fatalf("unexpected successful delivery state: %+v", jobs[0])
	}
}

func TestRetryLimitAndExplicitRetry(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "DO-NOT-STORE-THIS-SECRET")
	}))
	defer server.Close()
	s := testService(t, openTestDB(t, filepath.Join(t.TempDir(), "outbox.sqlite")))
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	queueEvent(t, s, testEvent("retry-limit"), Config{Webhook: WebhookConfig{Enabled: true, URL: server.URL + "?token=DO-NOT-STORE-THIS-SECRET", Secret: "DO-NOT-STORE-THIS-SECRET"}})
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if found, err := s.deliverNext(context.Background()); err != nil || !found {
			t.Fatalf("attempt %d: found=%v err=%v", attempt, found, err)
		}
		now = now.Add(time.Hour)
	}
	if found, err := s.deliverNext(context.Background()); err != nil || found {
		t.Fatalf("delivery exceeded maximum attempts: found=%v err=%v", found, err)
	}
	jobs, _ := s.List(context.Background())
	job := jobs[0]
	if job.Status != "failed" || job.Attempts != maxAttempts || strings.Contains(job.LastError, "SECRET") {
		t.Fatalf("unexpected exhausted delivery state: %+v", job)
	}
	if err := s.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.List(context.Background())
	if jobs[0].ID != job.ID || jobs[0].Status != "pending" || jobs[0].Attempts != 0 {
		t.Fatalf("explicit retry did not preserve identity/reset attempts: %+v", jobs[0])
	}
	if err := s.Retry(context.Background(), job.ID); err == nil {
		t.Fatal("pending delivery accepted another retry")
	}
}

func TestRestartRecoversInterruptedDelivery(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	delivered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- r.Header.Get("X-Donate-Delivery")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "outbox.sqlite")
	db := openTestDB(t, path)
	s := testService(t, db)
	queueEvent(t, s, testEvent("interrupted"), Config{Webhook: WebhookConfig{Enabled: true, URL: server.URL, Secret: "restart-secret"}})
	claimed, found, err := s.claim(context.Background())
	if err != nil || !found {
		t.Fatalf("claim before restart: found=%v err=%v", found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s = testService(t, openTestDB(t, path))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	select {
	case id := <-delivered:
		if id != claimed.ID {
			t.Fatal("recovered job changed its delivery identity")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupted delivery was not recovered after restart")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		jobs, err := s.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if jobs[0].Status == "delivered" {
			if jobs[0].Attempts != 2 {
				t.Fatalf("interrupted attempt was not retained: %+v", jobs[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered delivery acknowledgement was not persisted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on context cancellation")
	}
}

func TestWebhookDestinationPolicyAndRedirects(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "")
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "::ffff:127.0.0.1", "2001:db8::1", "2002:7f00:1::"} {
		cfg := Config{Webhook: WebhookConfig{Enabled: true, URL: "https://" + net.JoinHostPort(address, "443") + "/hook", Secret: "secret"}}
		if err := Validate(cfg); err == nil {
			t.Errorf("unsafe destination %s was accepted", address)
		}
	}
	for _, raw := range []string{"http://8.8.8.8/hook", "https://user:secret@8.8.8.8/hook", "https://8.8.8.8/hook#token", "https://8.8.8.8:0/hook"} {
		if err := Validate(Config{Webhook: WebhookConfig{Enabled: true, URL: raw, Secret: "secret"}}); err == nil {
			t.Errorf("unsafe URL %s was accepted", raw)
		}
	}
	if err := Validate(Config{Webhook: WebhookConfig{Enabled: true, URL: "https://8.8.8.8/hook", Secret: "secret"}}); err != nil {
		t.Fatalf("public HTTPS destination was rejected: %v", err)
	}
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	if err := Validate(Config{Webhook: WebhookConfig{Enabled: true, URL: "http://8.8.8.8/hook", Secret: "secret"}}); err == nil {
		t.Fatal("development flag permitted a public plaintext webhook")
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	body, _ := json.Marshal(testEvent("redirect"))
	err := sendWebhook(context.Background(), "delivery", body, WebhookConfig{Enabled: true, URL: redirect.URL, Secret: "secret"})
	if err == nil || redirected.Load() != 0 {
		t.Fatalf("webhook redirect was followed: hits=%d err=%v", redirected.Load(), err)
	}
}

func TestSMTPDeliversOnlyToConfiguredOwner(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	messages := make(chan string, 1)
	errors := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errors <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.WriteString(conn, "220 development SMTP\r\n")
		scanner := bufio.NewScanner(conn)
		inBody := false
		var message strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if inBody {
				if line == "." {
					inBody = false
					messages <- message.String()
					_, _ = io.WriteString(conn, "250 queued\r\n")
				} else {
					message.WriteString(line + "\n")
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				_, _ = io.WriteString(conn, "250-development SMTP\r\n250 8BITMIME\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				_, _ = io.WriteString(conn, "250 sender accepted\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				if line != "RCPT TO:<owner@example.org>" {
					t.Errorf("notification was sent to unexpected recipient %q", line)
				}
				_, _ = io.WriteString(conn, "250 recipient accepted\r\n")
			case line == "DATA":
				inBody = true
				_, _ = io.WriteString(conn, "354 send message\r\n")
			case line == "QUIT":
				_, _ = io.WriteString(conn, "221 goodbye\r\n")
				return
			default:
				_, _ = io.WriteString(conn, "500 unsupported\r\n")
			}
		}
		errors <- scanner.Err()
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	body, _ := json.Marshal(testEvent("email"))
	err = sendSMTP(context.Background(), body, SMTPConfig{
		Enabled: true, Host: host, Port: portNumber, From: "TOKEN <donate@example.org>", To: "owner@example.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-messages:
		if !strings.Contains(message, "To: <owner@example.org>") || !strings.Contains(message, `"amount_minor":1200`) || !strings.Contains(message, "donation.completed") {
			t.Fatalf("owner notification omitted expected data: %s", message)
		}
	case err := <-errors:
		t.Fatalf("SMTP server failed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("SMTP notification was not delivered")
	}
}

type smtpTestDelivery struct {
	message   string
	recipient string
	err       error
}

// The server completes TLS before emitting its SMTP greeting. It deliberately
// advertises STARTTLS as well, so an erroneous second TLS negotiation fails.
func implicitTLSTestServer(t *testing.T) (net.Listener, *x509.CertPool, <-chan smtpTestDelivery) {
	t.Helper()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverTLS := fixture.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := tls.NewListener(raw, serverTLS)
	t.Cleanup(func() { _ = listener.Close() })
	results := make(chan smtpTestDelivery, 1)
	go func() {
		var result smtpTestDelivery
		defer func() { results <- result }()
		conn, err := listener.Accept()
		if err != nil {
			result.err = err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := conn.(*tls.Conn).Handshake(); err != nil {
			result.err = err
			return
		}
		_, _ = io.WriteString(conn, "220 TLS SMTP\r\n")
		scanner := bufio.NewScanner(conn)
		inBody := false
		var message strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if inBody {
				if line == "." {
					inBody = false
					result.message = message.String()
					_, _ = io.WriteString(conn, "250 queued\r\n")
				} else {
					message.WriteString(line + "\n")
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				_, _ = io.WriteString(conn, "250-TLS SMTP\r\n250-8BITMIME\r\n250 STARTTLS\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				_, _ = io.WriteString(conn, "250 sender accepted\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				result.recipient = line
				_, _ = io.WriteString(conn, "250 recipient accepted\r\n")
			case line == "DATA":
				inBody = true
				_, _ = io.WriteString(conn, "354 send message\r\n")
			case line == "QUIT":
				_, _ = io.WriteString(conn, "221 goodbye\r\n")
				return
			default:
				_, _ = io.WriteString(conn, "500 unsupported\r\n")
			}
		}
		result.err = scanner.Err()
	}()
	return listener, roots, results
}

func TestSMTPPort465NegotiatesTLSBeforeGreetingAndNotifiesOwner(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	listener, roots, results := implicitTLSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// An ephemeral local port transports the test; the production SMTP helper
	// receives the standard configured port 465 and must select implicit TLS.
	conn, err := dialAllowed(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	cfg := SMTPConfig{Enabled: true, Host: "127.0.0.1", Port: 465, From: "donate@example.org", To: "owner@example.org"}
	client, err := newSMTPClient(ctx, conn, cfg, &tls.Config{ServerName: cfg.Host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("implicit TLS negotiation failed: %v", err)
	}
	defer client.Close()
	state, secure := client.TLSConnectionState()
	if !secure || len(state.VerifiedChains) == 0 {
		t.Fatal("SMTP session did not use a verified TLS connection")
	}
	event := testEvent("implicit-tls")
	body, _ := json.Marshal(event)
	if err := sendSMTPMessage(client, body, event, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		if result.err != nil || result.recipient != "RCPT TO:<owner@example.org>" || !strings.Contains(result.message, `"amount_minor":1200`) {
			t.Fatalf("implicit TLS owner delivery failed: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("implicit TLS server did not receive the owner notification")
	}
}

func TestSMTPPort465RejectsWrongCertificateHostname(t *testing.T) {
	t.Setenv("DONATE_ALLOW_PRIVATE_WEBHOOKS", "true")
	listener, roots, _ := implicitTLSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialAllowed(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cfg := SMTPConfig{Host: "wrong.smtp.example", Port: 465}
	client, err := newSMTPClient(ctx, conn, cfg, &tls.Config{ServerName: cfg.Host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err == nil {
		_ = client.Close()
		t.Fatal("implicit TLS accepted a certificate for another hostname")
	}
	if err.Error() != "SMTP secure connection failed" {
		t.Fatalf("unexpected or unsafe TLS error: %v", err)
	}
}
