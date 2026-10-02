package donors

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
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/auth"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	_ "modernc.org/sqlite"
)

const testOrigin = "https://donate.example"

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
		AllowCredentials   []any  `json:"allowCredentials"`
		ExcludeCredentials []any  `json:"excludeCredentials"`
		UserVerification   string `json:"userVerification"`
	} `json:"publicKey"`
}

type donorBrowser struct {
	t       *testing.T
	m       *Manager
	mux     *http.ServeMux
	cookies map[string]*http.Cookie
	csrf    string
}

func newBrowser(t *testing.T) *donorBrowser {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "donors.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	m, err := New(db, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b := &donorBrowser{t: t, m: m, cookies: make(map[string]*http.Cookie)}
	b.routes()
	return b
}

func (b *donorBrowser) routes() {
	b.mux = http.NewServeMux()
	b.mux.HandleFunc("GET /api/donor/session", b.m.Session)
	b.mux.HandleFunc("POST /api/donor/register/begin", b.m.RegisterBegin)
	b.mux.HandleFunc("POST /api/donor/register/finish", b.m.RegisterFinish)
	b.mux.HandleFunc("POST /api/donor/login/begin", b.m.LoginBegin)
	b.mux.HandleFunc("POST /api/donor/login/finish", b.m.LoginFinish)
	b.mux.HandleFunc("POST /api/donor/logout", b.m.Logout)
}

func (b *donorBrowser) another() *donorBrowser {
	return &donorBrowser{t: b.t, m: b.m, mux: b.mux, cookies: make(map[string]*http.Cookie)}
}

func (b *donorBrowser) clone() *donorBrowser {
	o := b.another()
	o.csrf = b.csrf
	for k, v := range b.cookies {
		cp := *v
		o.cookies[k] = &cp
	}
	return o
}

func jsonBody(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (b *donorBrowser) request(method, path string, body any, overrides ...map[string]string) *httptest.ResponseRecorder {
	b.t.Helper()
	var raw []byte
	if body != nil {
		if data, ok := body.([]byte); ok {
			raw = data
		} else {
			raw = jsonBody(b.t, body)
		}
	}
	r := httptest.NewRequest(method, testOrigin+path, bytes.NewReader(raw))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	if b.csrf != "" {
		r.Header.Set("X-CSRF-Token", b.csrf)
	}
	for _, override := range overrides {
		for k, v := range override {
			r.Header.Set(k, v)
		}
	}
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.mux.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
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

func (b *donorBrowser) status(w *httptest.ResponseRecorder) sessionStatus {
	b.t.Helper()
	requireHTTP(b.t, w, 200)
	var status sessionStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		b.t.Fatal(err)
	}
	b.csrf = status.CSRFToken
	return status
}

func (b *donorBrowser) begin(kind string) ceremonyOptions {
	b.t.Helper()
	w := b.request("POST", "/api/donor/"+kind+"/begin", map[string]any{})
	requireHTTP(b.t, w, 200)
	var options ceremonyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		b.t.Fatal(err)
	}
	if options.PublicKey.Challenge == "" {
		b.t.Fatal("missing WebAuthn challenge")
	}
	return options
}

type testPasskey struct {
	id         []byte
	private    *ecdsa.PrivateKey
	userHandle string
	counter    uint32
}

func newPasskey(t *testing.T, scalar int64) *testPasskey {
	t.Helper()
	curve := elliptic.P256()
	d := big.NewInt(scalar)
	x, y := curve.ScalarBaseMult(d.Bytes())
	return &testPasskey{id: []byte("donor-test-" + t.Name() + "-" + d.String()), private: &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}}
}

func encodeURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func authData(rp string, flags protocol.AuthenticatorFlags, count uint32, trailing []byte) []byte {
	h := sha256.Sum256([]byte(rp))
	raw := append([]byte{}, h[:]...)
	raw = append(raw, byte(flags))
	raw = binary.BigEndian.AppendUint32(raw, count)
	return append(raw, trailing...)
}

// Synthetic hardware creates real CBOR attestation objects and ECDSA assertion
// signatures. Nothing bypasses the WebAuthn verifier or trusts inserted keys.
func (p *testPasskey) registration(t *testing.T, options ceremonyOptions, uv bool) []byte {
	t.Helper()
	p.userHandle = options.PublicKey.User.ID
	key, err := webauthncbor.Marshal(map[int64]any{1: int64(2), 3: int64(-7), -1: int64(1), -2: p.private.X.FillBytes(make([]byte, 32)), -3: p.private.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	trailing := binary.BigEndian.AppendUint16(make([]byte, 16), uint16(len(p.id)))
	trailing = append(trailing, p.id...)
	trailing = append(trailing, key...)
	flags := protocol.FlagUserPresent | protocol.FlagAttestedCredentialData
	if uv {
		flags |= protocol.FlagUserVerified
	}
	object, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData(options.PublicKey.RP.ID, flags, 0, trailing)})
	if err != nil {
		t.Fatal(err)
	}
	return jsonBody(t, map[string]any{"id": encodeURL(p.id), "rawId": encodeURL(p.id), "type": "public-key", "response": map[string]any{"attestationObject": encodeURL(object), "clientDataJSON": encodeURL(jsonBody(t, map[string]any{"type": "webauthn.create", "challenge": options.PublicKey.Challenge, "origin": testOrigin, "crossOrigin": false}))}})
}

func (p *testPasskey) assertion(t *testing.T, options ceremonyOptions, uv bool, origin string, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	p.counter++
	flags := protocol.FlagUserPresent
	if uv {
		flags |= protocol.FlagUserVerified
	}
	rawAuth := authData(options.PublicKey.RPID, flags, p.counter, nil)
	rawClient := jsonBody(t, map[string]any{"type": "webauthn.get", "challenge": options.PublicKey.Challenge, "origin": origin, "crossOrigin": false})
	clientHash := sha256.Sum256(rawClient)
	digest := sha256.Sum256(append(append([]byte{}, rawAuth...), clientHash[:]...))
	if signer == nil {
		signer = p.private
	}
	signature, err := ecdsa.SignASN1(rand.Reader, signer, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return jsonBody(t, map[string]any{"id": encodeURL(p.id), "rawId": encodeURL(p.id), "type": "public-key", "response": map[string]any{"authenticatorData": encodeURL(rawAuth), "clientDataJSON": encodeURL(rawClient), "signature": encodeURL(signature), "userHandle": p.userHandle}})
}

func (b *donorBrowser) enroll(p *testPasskey) sessionStatus {
	b.t.Helper()
	options := b.begin("register")
	body := p.registration(b.t, options, true)
	s := b.status(b.request("POST", "/api/donor/register/finish", body))
	if !s.Authenticated || s.User == nil || s.User.ID == "" || s.CSRFToken == "" || s.PasskeyCount < 1 || s.User.CreatedAt == 0 {
		b.t.Fatalf("invalid enrolled session: %+v", s)
	}
	return s
}

func TestDonorRegistrationPersistenceBackupAndLogout(t *testing.T) {
	b := newBrowser(t)
	if s := b.status(b.request("GET", "/api/donor/session", nil)); s.Authenticated || s.User != nil || s.CSRFToken != "" {
		t.Fatalf("new visitor got a donor: %+v", s)
	}
	p := newPasskey(t, 11)
	options := b.begin("register")
	var count int
	if err := b.m.db.QueryRow("SELECT COUNT(*) FROM donor_users").Scan(&count); err != nil || count != 0 {
		t.Fatalf("begin created identity: %d %v", count, err)
	}
	// A server restart does not discard a pending ceremony or its browser binding.
	reopened, err := New(b.m.db, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b.m = reopened
	b.routes()
	first := b.status(b.request("POST", "/api/donor/register/finish", p.registration(t, options, true)))
	oldToken := b.cookies[sessionCookie].Value
	for _, cookie := range b.cookies {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("unsafe cookie: %+v", cookie)
		}
	}
	var stored string
	if err := b.m.db.QueryRow("SELECT token_hash FROM donor_sessions").Scan(&stored); err != nil || stored == oldToken || stored != tokenHash(oldToken) {
		t.Fatalf("session stored raw token: %v", err)
	}
	backup := newPasskey(t, 12)
	second := b.enroll(backup)
	if second.DonorID != first.DonorID || backup.userHandle != p.userHandle || second.PasskeyCount != 2 {
		t.Fatalf("backup created a different identity: %+v %+v", first, second)
	}
	if b.cookies[sessionCookie].Value == oldToken {
		t.Fatal("credential enrollment did not rotate session")
	}
	stale := b.another()
	stale.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: oldToken}
	r := httptest.NewRequest("GET", testOrigin+"/", nil)
	r.AddCookie(stale.cookies[sessionCookie])
	if _, err := b.m.Current(r); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("rotated session still identifies donor: %v", err)
	}
	requireHTTP(t, b.request("POST", "/api/donor/logout", map[string]any{}, map[string]string{"X-CSRF-Token": "wrong"}), 401)
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	if b.cookies[sessionCookie] != nil {
		t.Fatal("logout retained session cookie")
	}
	login := b.begin("login")
	if len(login.PublicKey.AllowCredentials) != 0 || login.PublicKey.UserVerification != "required" {
		t.Fatalf("login is not discoverable with UV: %+v", login)
	}
	logged := b.status(b.request("POST", "/api/donor/login/finish", backup.assertion(t, login, true, testOrigin, nil)))
	if logged.DonorID != first.DonorID || logged.PasskeyCount != 2 {
		t.Fatalf("backup did not recover same donor: %+v", logged)
	}
	other := b.another()
	if s := other.status(other.request("GET", "/api/donor/session", nil)); s.Authenticated || s.PasskeyCount != 0 || s.DonorID != "" {
		t.Fatalf("anonymous session disclosed donor: %+v", s)
	}
}

func TestDonorOriginCryptoUVAndCredentialOwner(t *testing.T) {
	b := newBrowser(t)
	p := newPasskey(t, 21)
	for _, origin := range []string{"", "null", "https://attacker.example", testOrigin + "/", testOrigin + "?x=1"} {
		requireHTTP(t, b.request("POST", "/api/donor/register/begin", map[string]any{}, map[string]string{"Origin": origin}), 403)
	}
	begin := b.begin("register")
	invalid := p.registration(t, begin, false)
	requireHTTP(t, b.request("POST", "/api/donor/register/finish", invalid), 401)
	requireHTTP(t, b.request("POST", "/api/donor/register/finish", invalid), 401)
	var count int
	_ = b.m.db.QueryRow("SELECT COUNT(*) FROM donor_users").Scan(&count)
	if count != 0 {
		t.Fatal("registration without UV created an identity")
	}
	first := b.enroll(p)
	requireHTTP(t, b.request("POST", "/api/donor/register/begin", map[string]any{}, map[string]string{"X-CSRF-Token": ""}), 401)
	secondBrowser := b.another()
	q := newPasskey(t, 22)
	second := secondBrowser.enroll(q)
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	for _, test := range []struct {
		name, origin string
		uv           bool
		signer       *ecdsa.PrivateKey
		handle       string
	}{
		{"no verification", testOrigin, false, nil, p.userHandle},
		{"wrong origin", "https://attacker.example", true, nil, p.userHandle},
		{"wrong signer", testOrigin, true, q.private, p.userHandle},
		{"another user", testOrigin, true, nil, q.userHandle},
		{"missing user handle", testOrigin, true, nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			login := b.begin("login")
			handle := p.userHandle
			p.userHandle = test.handle
			body := p.assertion(t, login, test.uv, test.origin, test.signer)
			p.userHandle = handle
			w := b.request("POST", "/api/donor/login/finish", body)
			requireHTTP(t, w, 401)
			if w.Body.String() != "{\"error\":\"Passkey 验证失败，请重试\"}\n" {
				t.Fatalf("authentication error reveals failure details: %s", w.Body.String())
			}
			requireHTTP(t, b.request("POST", "/api/donor/login/finish", body), 401)
		})
	}
	login := b.begin("login")
	success := b.status(b.request("POST", "/api/donor/login/finish", p.assertion(t, login, true, testOrigin, nil)))
	if success.DonorID != first.DonorID || success.DonorID == second.DonorID {
		t.Fatal("login accepted a different credential owner")
	}
}

func TestDonorChallengeExpiryBindingReplacementAndConcurrentConsume(t *testing.T) {
	b := newBrowser(t)
	p := newPasskey(t, 31)
	options := b.begin("register")
	body := p.registration(t, options, true)
	_ = b.begin("register")
	requireHTTP(t, b.request("POST", "/api/donor/register/finish", body), 401)
	options = b.begin("register")
	body = p.registration(t, options, true)
	if _, err := b.m.db.Exec("UPDATE donor_challenges SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireHTTP(t, b.request("POST", "/api/donor/register/finish", body), 401)
	options = b.begin("register")
	body = p.registration(t, options, true)
	left, right := b.clone(), b.clone()
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, browser := range []*donorBrowser{left, right} {
		wg.Add(1)
		go func(browser *donorBrowser) {
			defer wg.Done()
			results <- browser.request("POST", "/api/donor/register/finish", body).Code
		}(browser)
	}
	wg.Wait()
	close(results)
	success := 0
	rejected := 0
	for code := range results {
		if code == 200 {
			success++
		} else if code == 401 {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent response %d", code)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("challenge replay succeeded: success %d rejected %d", success, rejected)
	}
	// Logged-in registration is bound to the exact session from its begin.
	active := left
	if active.cookies[sessionCookie] == nil {
		active = right
	}
	active.status(active.request("GET", "/api/donor/session", nil))
	backup := newPasskey(t, 32)
	add := active.begin("register")
	addBody := backup.registration(t, add, true)
	wrongContext := active.clone()
	delete(wrongContext.cookies, sessionCookie)
	requireHTTP(t, wrongContext.request("POST", "/api/donor/register/finish", addBody), 401)
	requireHTTP(t, active.request("POST", "/api/donor/register/finish", addBody), 401)
	add = active.begin("register")
	addBody = backup.registration(t, add, true)
	if _, err := b.m.db.Exec("UPDATE donor_sessions SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireHTTP(t, active.request("POST", "/api/donor/register/finish", addBody), 401)
	status := active.status(active.request("GET", "/api/donor/session", nil))
	if status.Authenticated || active.cookies[sessionCookie] != nil {
		t.Fatal("expired session retained donor identity")
	}
}

func TestDonorAndAdministratorNeverShareAuthorityOrReset(t *testing.T) {
	b := newBrowser(t)
	admin, err := auth.New(b.m.db, t.TempDir(), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	admin.Routes(b.mux)
	b.mux.HandleFunc("GET /protected", admin.Require(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	p := newPasskey(t, 41)
	registered := b.enroll(p)
	requireHTTP(t, b.request("GET", "/protected", nil), 401)
	password, err := admin.Password()
	if err != nil {
		t.Fatal(err)
	}
	requireHTTP(t, b.request("POST", "/api/auth/password", map[string]string{"password": password}), 200)
	adminCookie := b.cookies["__Host-donate_session"]
	if adminCookie == nil {
		t.Fatal("admin bootstrap did not set its independent cookie")
	}
	adminToken := adminCookie.Value
	if _, err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	if current := b.status(b.request("GET", "/api/donor/session", nil)); !current.Authenticated || current.DonorID != registered.DonorID {
		t.Fatal("admin reset revoked donor")
	}
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	if b.cookies["__Host-donate_session"].Value != adminToken {
		t.Fatal("donor logout cleared or changed admin cookie")
	}
	r := httptest.NewRequest("GET", testOrigin+"/", nil)
	r.AddCookie(b.cookies["__Host-donate_session"])
	if user, err := b.m.Current(r); err != nil || user != nil {
		t.Fatalf("administrator cookie identified a donor: %+v %v", user, err)
	}
}

func TestDonorAuthorizeRejectsStaleIdentityAndAdminToken(t *testing.T) {
	b := newBrowser(t)
	p := newPasskey(t, 51)
	registered := b.enroll(p)
	r := httptest.NewRequest("POST", testOrigin+"/", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("X-CSRF-Token", b.csrf)
	r.AddCookie(b.cookies[sessionCookie])
	if user, err := b.m.Authorize(r); err != nil || user.ID != registered.DonorID {
		t.Fatalf("valid donor rejected: %v", err)
	}
	for _, change := range []map[string]string{{"Origin": "https://attacker.example"}, {"X-CSRF-Token": "admin-token"}} {
		clone := r.Clone(r.Context())
		clone.Header = r.Header.Clone()
		for k, v := range change {
			clone.Header.Set(k, v)
		}
		if _, err := b.m.Authorize(clone); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("wrong origin/CSRF authorized donor: %v", err)
		}
	}
	if _, err := b.m.db.Exec("UPDATE donor_sessions SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.m.Current(r); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired cookie silently became guest: %v", err)
	}
	guest := httptest.NewRequest("POST", testOrigin+"/", nil)
	if user, err := b.m.Current(guest); user != nil || err != nil {
		t.Fatalf("anonymous request rejected: %v", err)
	}
}

func TestDonorLoginPersistenceCounterAndExpiry(t *testing.T) {
	b := newBrowser(t)
	p := newPasskey(t, 61)
	first := b.enroll(p)
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	options := b.begin("login")
	reopened, err := New(b.m.db, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b.m = reopened
	b.routes()
	logged := b.status(b.request("POST", "/api/donor/login/finish", p.assertion(t, options, true, testOrigin, nil)))
	if logged.DonorID != first.DonorID {
		t.Fatal("login challenge lost its identity across restart")
	}
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	options = b.begin("login")
	p.counter = 0 // A cloned authenticator repeats an already accepted counter.
	requireHTTP(t, b.request("POST", "/api/donor/login/finish", p.assertion(t, options, true, testOrigin, nil)), 401)
	if b.cookies[sessionCookie] != nil {
		t.Fatal("cloned assertion issued a donor session")
	}
	options = b.begin("login")
	p.counter = 1
	logged = b.status(b.request("POST", "/api/donor/login/finish", p.assertion(t, options, true, testOrigin, nil)))
	if logged.DonorID != first.DonorID {
		t.Fatal("new authenticator counter could not log in")
	}
	b.status(b.request("POST", "/api/donor/logout", map[string]any{}))
	options = b.begin("login")
	if _, err := b.m.db.Exec("UPDATE donor_challenges SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	requireHTTP(t, b.request("POST", "/api/donor/login/finish", p.assertion(t, options, true, testOrigin, nil)), 401)
}

func TestDonorCanonicalOriginsKeepExactBoundary(t *testing.T) {
	b := newBrowser(t)
	for _, test := range []struct{ public, accepted, rejected string }{
		{"https://donate.example:443", "https://donate.example", "http://donate.example"},
		{"http://localhost:80", "http://localhost", "http://localhost:8080"},
		{"https://donate.example:8443", "https://donate.example:8443", "https://donate.example"},
	} {
		m, err := New(b.m.db, test.public)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", test.accepted+"/", nil)
		r.Header.Set("Origin", test.accepted)
		if !m.CheckOrigin(r) {
			t.Fatalf("rejected matching canonical origin %s", test.accepted)
		}
		r.Header.Set("Origin", test.rejected)
		if m.CheckOrigin(r) {
			t.Fatalf("accepted different origin %s", test.rejected)
		}
	}
	for _, origin := range []string{"https://donate.example/path", "https://donate.example?query=x", "https://user@donate.example", "http://donate.example", "https://127.0.0.1", "file:///tmp/x"} {
		if _, err := New(b.m.db, origin); err == nil {
			t.Fatalf("invalid public origin accepted: %s", origin)
		}
	}
}
