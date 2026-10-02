package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	_ "modernc.org/sqlite"
)

const testOrigin = "https://donate.example"

type testStatus struct {
	Initialized   bool   `json:"initialized"`
	Authenticated bool   `json:"authenticated"`
	NeedsPasskey  bool   `json:"needs_passkey"`
	CSRF          string `json:"csrf_token"`
	PasskeyCount  int    `json:"passkey_count"`
}

type ceremonyOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		RPID string `json:"rpId"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

type authBrowser struct {
	t       *testing.T
	manager *Manager
	mux     *http.ServeMux
	cookies map[string]*http.Cookie
	csrf    string
}

func newAuthBrowser(t *testing.T) *authBrowser {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	m, err := New(db, dir, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Routes(mux)
	mux.HandleFunc("/test/admin", m.Require(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	return &authBrowser{t: t, manager: m, mux: mux, cookies: make(map[string]*http.Cookie)}
}

func (b *authBrowser) anotherBrowser() *authBrowser {
	return &authBrowser{t: b.t, manager: b.manager, mux: b.mux, cookies: make(map[string]*http.Cookie)}
}

func (b *authBrowser) request(method, path string, body any, overrides ...map[string]string) *httptest.ResponseRecorder {
	b.t.Helper()
	var raw []byte
	if body != nil {
		if bytesBody, ok := body.([]byte); ok {
			raw = bytesBody
		} else {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				b.t.Fatal(err)
			}
		}
	}
	r := httptest.NewRequest(method, testOrigin+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", testOrigin)
	if b.csrf != "" {
		r.Header.Set("X-CSRF-Token", b.csrf)
	}
	for _, override := range overrides {
		for key, value := range override {
			if value == "" {
				r.Header.Del(key)
			} else {
				r.Header.Set(key, value)
			}
		}
	}
	for _, cookie := range b.cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	b.mux.ServeHTTP(w, r)
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	return w
}

func requireHTTP(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, code, w.Body.String())
	}
}

func requireRejected(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("request must be rejected without a server error, got HTTP %d: %s", w.Code, w.Body.String())
	}
}

func (b *authBrowser) readStatus(w *httptest.ResponseRecorder) testStatus {
	b.t.Helper()
	requireHTTP(b.t, w, http.StatusOK)
	var s testStatus
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		b.t.Fatal(err)
	}
	b.csrf = s.CSRF
	return s
}

func (b *authBrowser) bootstrap() string {
	b.t.Helper()
	password, err := b.manager.Password()
	if err != nil {
		b.t.Fatal(err)
	}
	s := b.readStatus(b.request(http.MethodPost, "/api/auth/password", map[string]string{"password": password}))
	if s.Initialized || s.Authenticated || !s.NeedsPasskey || s.CSRF == "" {
		b.t.Fatalf("bootstrap session has wrong privileges: %+v", s)
	}
	return password
}

func (b *authBrowser) begin(path string) ceremonyOptions {
	b.t.Helper()
	w := b.request(http.MethodPost, path, map[string]any{})
	requireHTTP(b.t, w, http.StatusOK)
	var options ceremonyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		b.t.Fatal(err)
	}
	if options.PublicKey.Challenge == "" {
		b.t.Fatalf("missing WebAuthn challenge: %s", w.Body.String())
	}
	return options
}

type testPasskey struct {
	id         []byte
	private    *ecdsa.PrivateKey
	userHandle string
}

// This authenticator produces real CBOR attestation and ECDSA signatures. It
// exercises WebAuthn verification rather than inserting a trusted database row.
func newTestPasskey(t *testing.T, scalar int64) *testPasskey {
	t.Helper()
	curve := elliptic.P256()
	d := big.NewInt(scalar)
	x, y := curve.ScalarBaseMult(d.Bytes())
	return &testPasskey{
		id: []byte("synthetic-passkey-" + t.Name() + "-" + d.String()),
		private: &ecdsa.PrivateKey{
			PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d,
		},
	}
}

func encodeURL(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func authData(rpID string, flags protocol.AuthenticatorFlags, counter uint32, trailing []byte) []byte {
	hash := sha256.Sum256([]byte(rpID))
	raw := append([]byte{}, hash[:]...)
	raw = append(raw, byte(flags))
	raw = binary.BigEndian.AppendUint32(raw, counter)
	return append(raw, trailing...)
}

func clientData(t *testing.T, kind, challenge, origin string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func marshalTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (p *testPasskey) registration(t *testing.T, options ceremonyOptions, verified bool) []byte {
	t.Helper()
	p.userHandle = options.PublicKey.User.ID
	publicKey, err := webauthncbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1),
		-2: p.private.PublicKey.X.FillBytes(make([]byte, 32)),
		-3: p.private.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 16) // Anonymous authenticator AAGUID.
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(p.id)))
	attested = append(attested, p.id...)
	attested = append(attested, publicKey...)
	flags := protocol.FlagUserPresent | protocol.FlagAttestedCredentialData
	if verified {
		flags |= protocol.FlagUserVerified
	}
	object, err := webauthncbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{},
		"authData": authData(options.PublicKey.RP.ID, flags, 0, attested),
	})
	if err != nil {
		t.Fatal(err)
	}
	return marshalTestJSON(t, map[string]any{
		"id": encodeURL(p.id), "rawId": encodeURL(p.id), "type": "public-key",
		"response": map[string]any{
			"attestationObject": encodeURL(object),
			"clientDataJSON":    encodeURL(clientData(t, "webauthn.create", options.PublicKey.Challenge, testOrigin)),
		},
	})
}

func (p *testPasskey) assertion(t *testing.T, options ceremonyOptions, verified bool, origin string, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	flags := protocol.FlagUserPresent
	if verified {
		flags |= protocol.FlagUserVerified
	}
	rawAuth := authData(options.PublicKey.RPID, flags, 1, nil)
	rawClient := clientData(t, "webauthn.get", options.PublicKey.Challenge, origin)
	clientHash := sha256.Sum256(rawClient)
	signed := append(append([]byte{}, rawAuth...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	if signer == nil {
		signer = p.private
	}
	signature, err := ecdsa.SignASN1(rand.Reader, signer, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return marshalTestJSON(t, map[string]any{
		"id": encodeURL(p.id), "rawId": encodeURL(p.id), "type": "public-key",
		"response": map[string]any{
			"authenticatorData": encodeURL(rawAuth), "clientDataJSON": encodeURL(rawClient),
			"signature": encodeURL(signature), "userHandle": p.userHandle,
		},
	})
}

func (b *authBrowser) enroll(p *testPasskey) []byte {
	b.t.Helper()
	options := b.begin("/api/auth/register/begin")
	body := p.registration(b.t, options, true)
	s := b.readStatus(b.request(http.MethodPost, "/api/auth/register/finish", body))
	if !s.Initialized || !s.Authenticated || s.NeedsPasskey || s.CSRF == "" || s.PasskeyCount < 1 {
		b.t.Fatalf("passkey enrollment did not issue full admin access: %+v", s)
	}
	return body
}

func TestBootstrapOriginAndPrivilegeBoundary(t *testing.T) {
	b := newAuthBrowser(t)
	s := b.readStatus(b.request(http.MethodGet, "/api/auth/status", nil))
	if s.Initialized || s.Authenticated || s.PasskeyCount != 0 {
		t.Fatalf("new installation is initialized or authenticated: %+v", s)
	}
	password, err := b.manager.Password()
	if err != nil || len(password) < 32 {
		t.Fatalf("bootstrap password is unavailable or too short: %v", err)
	}
	info, err := os.Stat(b.manager.bootstrapFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap file must be 0600: %v", err)
	}
	for _, origin := range []string{"", "https://attacker.example"} {
		requireHTTP(t, b.request(http.MethodPost, "/api/auth/password", map[string]string{"password": password}, map[string]string{"Origin": origin}), http.StatusForbidden)
	}
	requireHTTP(t, b.request(http.MethodPost, "/api/auth/password", map[string]string{"password": "incorrect"}), http.StatusUnauthorized)
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
	b.bootstrap()
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusForbidden)
	for _, headers := range []map[string]string{{"X-CSRF-Token": ""}, {"X-CSRF-Token": "wrong"}, {"Origin": "https://attacker.example"}} {
		requireHTTP(t, b.request(http.MethodPost, "/api/auth/register/begin", map[string]any{}, headers), http.StatusForbidden)
	}
	for _, cookie := range b.cookies {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("authentication cookie lacks required attributes: %+v", cookie)
		}
	}
	reopened, err := New(b.manager.db, filepath.Dir(b.manager.bootstrapFile), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Password()
	if err != nil || persisted != password {
		t.Fatalf("restart must preserve bootstrap secret: %v", err)
	}
}

func TestPasskeyLifecycleDisablesPasswordAndBlocksReplay(t *testing.T) {
	b := newAuthBrowser(t)
	password := b.bootstrap()
	p := newTestPasskey(t, 42)
	registration := b.enroll(p)
	if _, err := b.manager.Password(); err == nil {
		t.Fatal("CLI password access must be disabled once a passkey is bound")
	}
	if _, err := os.Stat(b.manager.bootstrapFile); !os.IsNotExist(err) {
		t.Fatalf("bound account must remove bootstrap secret file: %v", err)
	}
	requireHTTP(t, b.request(http.MethodPost, "/api/auth/password", map[string]string{"password": password}), http.StatusForbidden)
	requireRejected(t, b.request(http.MethodPost, "/api/auth/register/finish", registration))
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusNoContent)
	for _, headers := range []map[string]string{{"X-CSRF-Token": ""}, {"X-CSRF-Token": "wrong"}, {"Origin": "https://attacker.example"}, {"Origin": ""}} {
		requireHTTP(t, b.request(http.MethodPost, "/test/admin", map[string]any{}, headers), http.StatusForbidden)
	}
	requireHTTP(t, b.request(http.MethodPost, "/test/admin", map[string]any{}), http.StatusNoContent)
	w := b.request(http.MethodPost, "/api/auth/logout", map[string]any{})
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("logout failed: %d %s", w.Code, w.Body.String())
	}
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
	options := b.begin("/api/auth/login/begin")
	body := p.assertion(t, options, true, testOrigin, nil)
	s := b.readStatus(b.request(http.MethodPost, "/api/auth/login/finish", body))
	if !s.Authenticated || !s.Initialized || s.NeedsPasskey || s.PasskeyCount != 1 {
		t.Fatalf("valid passkey did not authenticate: %+v", s)
	}
	requireRejected(t, b.request(http.MethodPost, "/api/auth/login/finish", body))
}

func TestWebAuthnRequiresUserVerificationAndValidOriginSignature(t *testing.T) {
	b := newAuthBrowser(t)
	b.bootstrap()
	p := newTestPasskey(t, 43)
	options := b.begin("/api/auth/register/begin")
	requireHTTP(t, b.request(http.MethodPost, "/api/auth/register/finish", p.registration(t, options, false)), http.StatusBadRequest)
	// Even a failed finish consumes the challenge.
	requireRejected(t, b.request(http.MethodPost, "/api/auth/register/finish", p.registration(t, options, true)))
	b.enroll(p)
	guest := b.anotherBrowser()
	other := newTestPasskey(t, 44)
	for _, bad := range []struct {
		name     string
		verified bool
		origin   string
		signer   *ecdsa.PrivateKey
	}{
		{name: "user presence without verification", verified: false, origin: testOrigin},
		{name: "foreign signed client origin", verified: true, origin: "https://attacker.example"},
		{name: "foreign signing key", verified: true, origin: testOrigin, signer: other.private},
	} {
		t.Run(bad.name, func(t *testing.T) {
			options := guest.begin("/api/auth/login/begin")
			requireRejected(t, guest.request(http.MethodPost, "/api/auth/login/finish", p.assertion(t, options, bad.verified, bad.origin, bad.signer)))
			s := guest.readStatus(guest.request(http.MethodGet, "/api/auth/status", nil))
			if s.Authenticated {
				t.Fatal("invalid authenticator proof issued an admin session")
			}
			requireRejected(t, guest.request(http.MethodPost, "/api/auth/login/finish", p.assertion(t, options, true, testOrigin, nil)))
		})
	}
}

func TestChallengeExpirationAndBrowserBinding(t *testing.T) {
	b := newAuthBrowser(t)
	b.bootstrap()
	p := newTestPasskey(t, 45)
	options := b.begin("/api/auth/register/begin")
	if _, err := b.manager.db.Exec("UPDATE auth_challenges SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireRejected(t, b.request(http.MethodPost, "/api/auth/register/finish", p.registration(t, options, true)))
	b.enroll(p)
	guest := b.anotherBrowser()
	options = guest.begin("/api/auth/login/begin")
	body := p.assertion(t, options, true, testOrigin, nil)
	requireRejected(t, b.anotherBrowser().request(http.MethodPost, "/api/auth/login/finish", body))
	if _, err := b.manager.db.Exec("UPDATE auth_challenges SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireRejected(t, guest.request(http.MethodPost, "/api/auth/login/finish", body))
	if _, err := b.manager.db.Exec("UPDATE auth_sessions SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
}

func TestResetRotatesBootstrapAndRevokesSessionsPasskeysChallenges(t *testing.T) {
	b := newAuthBrowser(t)
	oldPassword := b.bootstrap()
	p := newTestPasskey(t, 46)
	b.enroll(p)
	guest := b.anotherBrowser()
	options := guest.begin("/api/auth/login/begin")
	assertion := p.assertion(t, options, true, testOrigin, nil)
	registrationOptions := b.begin("/api/auth/register/begin")
	registration := newTestPasskey(t, 48).registration(t, registrationOptions, true)
	newPassword, err := b.manager.Reset()
	if err != nil {
		t.Fatal(err)
	}
	if newPassword == oldPassword || len(newPassword) < 32 {
		t.Fatal("reset must rotate the random bootstrap secret")
	}
	retrieved, err := b.manager.Password()
	if err != nil || retrieved != newPassword {
		t.Fatalf("CLI must retrieve the reset password: %v", err)
	}
	for _, table := range []string{"auth_credentials", "auth_sessions", "auth_challenges"} {
		var count int
		if err := b.manager.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("reset retained authentication records in %s", table)
		}
	}
	s := b.readStatus(b.request(http.MethodGet, "/api/auth/status", nil))
	if s.Initialized || s.Authenticated || s.PasskeyCount != 0 {
		t.Fatalf("reset must remove bound credentials and authentication: %+v", s)
	}
	requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
	requireRejected(t, b.request(http.MethodPost, "/api/auth/register/finish", registration))
	requireRejected(t, guest.request(http.MethodPost, "/api/auth/login/finish", assertion))
	requireRejected(t, guest.request(http.MethodPost, "/api/auth/login/begin", map[string]any{}))
	requireHTTP(t, guest.request(http.MethodPost, "/api/auth/password", map[string]string{"password": oldPassword}), http.StatusUnauthorized)
	guest.readStatus(guest.request(http.MethodPost, "/api/auth/password", map[string]string{"password": newPassword}))
	guest.enroll(newTestPasskey(t, 47))
	requireHTTP(t, guest.request(http.MethodGet, "/test/admin", nil), http.StatusNoContent)
	for _, table := range []string{"auth_credentials", "auth_challenges"} {
		var count int
		if err := b.manager.db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE generation<(SELECT generation FROM auth_state WHERE id=1)").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("reset retained stale rows in %s", table)
		}
	}
}

func TestConcurrentAssertionFinishesIssueExactlyOneSession(t *testing.T) {
	b := newAuthBrowser(t)
	b.bootstrap()
	p := newTestPasskey(t, 49)
	b.enroll(p)
	guest := b.anotherBrowser()
	options := guest.begin("/api/auth/login/begin")
	body := p.assertion(t, options, true, testOrigin, nil)
	const workers = 4
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		browser := guest.anotherBrowser()
		for name, cookie := range guest.cookies {
			copy := *cookie
			browser.cookies[name] = &copy
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- browser.request(http.MethodPost, "/api/auth/login/finish", body)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.Code == http.StatusOK {
			successes++
		} else {
			requireRejected(t, result)
		}
	}
	if successes != 1 {
		t.Fatalf("one authenticator assertion issued %d sessions, want exactly 1", successes)
	}
}

func TestClonedCredentialRevokesSessionsWithoutReopeningPassword(t *testing.T) {
	for _, remainingPasskey := range []bool{false, true} {
		name := "requires CLI recovery"
		if remainingPasskey {
			name = "remaining passkey can recover"
		}
		t.Run(name, func(t *testing.T) {
			b := newAuthBrowser(t)
			password := b.bootstrap()
			p := newTestPasskey(t, 50)
			b.enroll(p)
			var fallback *testPasskey
			if remainingPasskey {
				fallback = newTestPasskey(t, 51)
				b.enroll(fallback)
			}
			guest := b.anotherBrowser()
			options := guest.begin("/api/auth/login/begin")
			s := guest.readStatus(guest.request(http.MethodPost, "/api/auth/login/finish", p.assertion(t, options, true, testOrigin, nil)))
			if !s.Authenticated {
				t.Fatal("first assertion was not accepted")
			}
			clone := b.anotherBrowser()
			options = clone.begin("/api/auth/login/begin")
			// A second freshly challenged, validly signed assertion returns the
			// same nonzero counter: this is a cloned authenticator signal.
			requireHTTP(t, clone.request(http.MethodPost, "/api/auth/login/finish", p.assertion(t, options, true, testOrigin, nil)), http.StatusUnauthorized)
			requireHTTP(t, b.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
			requireHTTP(t, guest.request(http.MethodGet, "/test/admin", nil), http.StatusUnauthorized)
			s = clone.readStatus(clone.request(http.MethodGet, "/api/auth/status", nil))
			wantCount := 0
			if remainingPasskey {
				wantCount = 1
			}
			if !s.Initialized || s.Authenticated || s.NeedsPasskey || s.PasskeyCount != wantCount {
				t.Fatalf("clone quarantine left incorrect auth state: %+v", s)
			}
			if _, err := b.manager.Password(); err == nil {
				t.Fatal("quarantine reopened CLI password access")
			}
			requireHTTP(t, clone.request(http.MethodPost, "/api/auth/password", map[string]string{"password": password}), http.StatusForbidden)
			if remainingPasskey {
				options := clone.begin("/api/auth/login/begin")
				s = clone.readStatus(clone.request(http.MethodPost, "/api/auth/login/finish", fallback.assertion(t, options, true, testOrigin, nil)))
				if !s.Authenticated {
					t.Fatal("remaining independent passkey could not recover admin access")
				}
			} else {
				requireHTTP(t, clone.request(http.MethodPost, "/api/auth/login/begin", map[string]any{}), http.StatusForbidden)
				if _, err := b.manager.Reset(); err != nil {
					t.Fatal(err)
				}
				clone.bootstrap()
				clone.enroll(newTestPasskey(t, 52))
			}
			requireHTTP(t, clone.request(http.MethodGet, "/test/admin", nil), http.StatusNoContent)
		})
	}
}

func TestPublicOriginValidation(t *testing.T) {
	for _, origin := range []string{"http://donate.example", "https://127.0.0.1", "https://user:secret@donate.example", "https://donate.example/admin", "https://donate.example?token=secret", "https://donate.example#fragment", "file:///tmp/admin"} {
		t.Run(origin, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := New(db, t.TempDir(), origin); err == nil {
				t.Fatalf("unsafe or invalid public origin accepted: %q", origin)
			}
		})
	}
}
