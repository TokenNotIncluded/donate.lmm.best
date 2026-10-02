package donors

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/auth"
	"github.com/go-webauthn/webauthn/webauthn"
)

var _ AdministratorPasskeys = (*auth.Manager)(nil)

type administratorStatus struct {
	Authenticated bool   `json:"authenticated"`
	Initialized   bool   `json:"initialized"`
	NeedsPasskey  bool   `json:"needs_passkey"`
	CSRFToken     string `json:"csrf_token"`
	PasskeyCount  int    `json:"passkey_count"`
}

func readAdministratorStatus(t *testing.T, response *httptest.ResponseRecorder) administratorStatus {
	t.Helper()
	requireHTTP(t, response, http.StatusOK)
	var status administratorStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func bridgeRoutes(b *donorBrowser, administrator *auth.Manager) {
	b.routes()
	administrator.Routes(b.mux)
	b.mux.HandleFunc("GET /protected", administrator.Require(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
}

func newAdministratorBrowser(t *testing.T) (*donorBrowser, *auth.Manager) {
	t.Helper()
	b := newBrowser(t)
	administrator, err := auth.New(b.m.db, t.TempDir(), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b.m, err = New(b.m.db, testOrigin, WithAdministratorPasskeys(administrator))
	if err != nil {
		t.Fatal(err)
	}
	bridgeRoutes(b, administrator)
	return b, administrator
}

func beginAt(t *testing.T, b *donorBrowser, path, csrf string) ceremonyOptions {
	t.Helper()
	w := b.request(http.MethodPost, path, map[string]any{}, map[string]string{"X-CSRF-Token": csrf})
	requireHTTP(t, w, http.StatusOK)
	var options ceremonyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	return options
}

func enrollAdministrator(t *testing.T, b *donorBrowser, administrator *auth.Manager, passkey *testPasskey) administratorStatus {
	t.Helper()
	status := readAdministratorStatus(t, b.request(http.MethodGet, "/api/auth/status", nil))
	if !status.Initialized {
		password, err := administrator.Password()
		if err != nil {
			t.Fatal(err)
		}
		status = readAdministratorStatus(t, b.request(http.MethodPost, "/api/auth/password", map[string]string{"password": password}))
	}
	options := beginAt(t, b, "/api/auth/register/begin", status.CSRFToken)
	w := b.request(http.MethodPost, "/api/auth/register/finish", passkey.registration(t, options, true), map[string]string{"X-CSRF-Token": status.CSRFToken})
	status = readAdministratorStatus(t, w)
	if !status.Authenticated || status.NeedsPasskey {
		t.Fatal("administrator enrollment did not authenticate")
	}
	return status
}

func loginDonorKey(t *testing.T, b *donorBrowser, key *testPasskey) sessionStatus {
	t.Helper()
	options := b.begin("login")
	return b.status(b.request(http.MethodPost, "/api/donor/login/finish", key.assertion(t, options, true, testOrigin, nil)))
}

func storedAdminKey(t *testing.T, db *sql.DB, key *testPasskey) webauthn.Credential {
	t.Helper()
	var raw []byte
	if err := db.QueryRow("SELECT data FROM auth_credentials WHERE id=?", encodeURL(key.id)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var credential webauthn.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

func TestAdministratorPasskeysIssueOnlyStableDonorSessions(t *testing.T) {
	b, administrator := newAdministratorBrowser(t)
	first, second := newPasskey(t, 101), newPasskey(t, 102)
	enrollAdministrator(t, b, administrator, first)
	enrollAdministrator(t, b, administrator, second)
	adminToken := b.cookies["__Host-donate_session"].Value
	guest := b.another()
	linked := loginDonorKey(t, guest, first)
	if !linked.Authenticated || linked.PasskeyCount != 2 || linked.DonorID == first.userHandle {
		t.Fatalf("invalid ordinary donor identity: %+v", linked)
	}
	if guest.cookies["__Host-donate_session"] != nil || readAdministratorStatus(t, guest.request(http.MethodGet, "/api/auth/status", nil)).Authenticated {
		t.Fatal("public passkey login granted administrator authority")
	}
	requireHTTP(t, guest.request(http.MethodGet, "/protected", nil), http.StatusUnauthorized)
	requireHTTP(t, b.request(http.MethodGet, "/protected", nil), http.StatusNoContent)
	if b.cookies["__Host-donate_session"].Value != adminToken {
		t.Fatal("donor login changed administrator session")
	}
	var nativeCount int
	if err := b.m.db.QueryRow("SELECT COUNT(*) FROM donor_credentials").Scan(&nativeCount); err != nil || nativeCount != 0 {
		t.Fatalf("administrator credential was copied into donor storage: %d %v", nativeCount, err)
	}
	if storedAdminKey(t, b.m.db, first).Authenticator.SignCount != first.counter {
		t.Fatal("public login did not update authoritative administrator counter")
	}
	guest.status(guest.request(http.MethodPost, "/api/donor/logout", map[string]any{}))
	reopened, err := New(b.m.db, testOrigin, WithAdministratorPasskeys(administrator))
	if err != nil {
		t.Fatal(err)
	}
	guest.m = reopened
	bridgeRoutes(guest, administrator)
	otherKey := loginDonorKey(t, guest, second)
	if otherKey.DonorID != linked.DonorID || otherKey.PasskeyCount != 2 {
		t.Fatal("another administrator key or restart created another donor identity")
	}
	backup := newPasskey(t, 103)
	backedUp := guest.enroll(backup)
	if backup.userHandle != linked.DonorID || backup.userHandle == first.userHandle || backedUp.DonorID != linked.DonorID || backedUp.PasskeyCount != 3 {
		t.Fatalf("public backup key acquired the wrong identity: %+v", backedUp)
	}
	guest.status(guest.request(http.MethodPost, "/api/donor/logout", map[string]any{}))
	backedUp = loginDonorKey(t, guest, backup)
	if backedUp.DonorID != linked.DonorID {
		t.Fatal("public backup did not retain the linked ordinary identity")
	}
	options := beginAt(t, guest, "/api/auth/login/begin", "")
	requireHTTP(t, guest.request(http.MethodPost, "/api/auth/login/finish", backup.assertion(t, options, true, testOrigin, nil)), http.StatusUnauthorized)
	if !guest.status(guest.request(http.MethodGet, "/api/donor/session", nil)).Authenticated {
		t.Fatal("rejected administrator login erased ordinary donor session")
	}
	// Login through the admin route advances the very same counter, rather than
	// an independent donor copy which would falsely report the device as cloned.
	options = beginAt(t, b, "/api/auth/login/begin", "")
	if !readAdministratorStatus(t, b.request(http.MethodPost, "/api/auth/login/finish", first.assertion(t, options, true, testOrigin, nil))).Authenticated {
		t.Fatal("administrator could not use a key previously used as a donor")
	}
	if storedAdminKey(t, b.m.db, first).Authenticator.SignCount != first.counter {
		t.Fatal("administrator and donor counters diverged")
	}
}

func TestAdministratorRecoveryBlocksOldKeyButPreservesDonorIdentity(t *testing.T) {
	b, administrator := newAdministratorBrowser(t)
	oldKey := newPasskey(t, 111)
	enrollAdministrator(t, b, administrator, oldKey)
	guest := b.another()
	linked := loginDonorKey(t, guest, oldKey)
	backup := newPasskey(t, 112)
	guest.enroll(backup)
	if _, err := administrator.Reset(); err != nil {
		t.Fatal(err)
	}
	current := guest.status(guest.request(http.MethodGet, "/api/donor/session", nil))
	if !current.Authenticated || current.DonorID != linked.DonorID || current.PasskeyCount != 1 {
		t.Fatal("administrator recovery erased donor history identity or backup key")
	}
	oldDevice := b.another()
	options := oldDevice.begin("login")
	requireHTTP(t, oldDevice.request(http.MethodPost, "/api/donor/login/finish", oldKey.assertion(t, options, true, testOrigin, nil)), http.StatusUnauthorized)
	backupDevice := b.another()
	if recovered := loginDonorKey(t, backupDevice, backup); recovered.DonorID != linked.DonorID {
		t.Fatal("ordinary backup key lost its identity during administrator reset")
	}
	newKey := newPasskey(t, 113)
	enrollAdministrator(t, b, administrator, newKey)
	if newKey.userHandle == oldKey.userHandle {
		t.Fatal("administrator recovery did not rotate its user handle")
	}
	newDevice := b.another()
	if recovered := loginDonorKey(t, newDevice, newKey); recovered.DonorID != linked.DonorID || recovered.PasskeyCount != 2 {
		t.Fatalf("replacement administrator key created another ordinary identity: %+v", recovered)
	}
}

func TestAdministratorDonorBridgeStillRequiresRealSignatureOriginUVAndHandle(t *testing.T) {
	b, administrator := newAdministratorBrowser(t)
	key, unknown := newPasskey(t, 121), newPasskey(t, 122)
	enrollAdministrator(t, b, administrator, key)
	wrongHandle, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, origin, handle string
		verified             bool
		wrongSigner          bool
	}{
		{"unverified", testOrigin, key.userHandle, false, false},
		{"wrong origin", "https://attacker.example", key.userHandle, true, false},
		{"wrong signature", testOrigin, key.userHandle, true, true},
		{"wrong handle", testOrigin, wrongHandle, true, false},
		{"missing handle", testOrigin, "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			guest := b.another()
			options := guest.begin("login")
			copy := *key
			copy.userHandle = test.handle
			var body []byte
			if test.wrongSigner {
				body = copy.assertion(t, options, test.verified, test.origin, unknown.private)
			} else {
				body = copy.assertion(t, options, test.verified, test.origin, nil)
			}
			requireHTTP(t, guest.request(http.MethodPost, "/api/donor/login/finish", body), http.StatusUnauthorized)
			requireHTTP(t, guest.request(http.MethodPost, "/api/donor/login/finish", body), http.StatusUnauthorized)
			if guest.cookies[sessionCookie] != nil || guest.cookies["__Host-donate_session"] != nil {
				t.Fatal("failed signature issued an authenticated cookie")
			}
		})
	}
	var count int
	if err = b.m.db.QueryRow("SELECT COUNT(*) FROM donor_identity_links").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed administrator proof created a donor identity link")
	}
	guest := b.another()
	options := guest.begin("login")
	body := key.assertion(t, options, true, testOrigin, nil)
	before := guest.clone()
	linked := guest.status(guest.request(http.MethodPost, "/api/donor/login/finish", body))
	if !linked.Authenticated {
		t.Fatal("valid administrator assertion was rejected")
	}
	requireHTTP(t, before.request(http.MethodPost, "/api/donor/login/finish", body), http.StatusUnauthorized)
}

// This wrapper pauses only the real authority's completed credential lookup.
// WebAuthn still verifies real signatures, and the real authority's transaction
// must reject a reset/deletion/counter update that occurred after its snapshot.
type pausedAdministrator struct {
	AdministratorPasskeys
	lookedUp chan struct{}
	resume   chan struct{}
}

func (p *pausedAdministrator) LookupDonorPasskey(ctx context.Context, id, handle []byte) (webauthn.User, error) {
	u, err := p.AdministratorPasskeys.LookupDonorPasskey(ctx, id, handle)
	if err == nil {
		close(p.lookedUp)
		<-p.resume
	}
	return u, err
}

func TestAdministratorDonorBridgeRechecksResetDeletionAndConcurrentAssertion(t *testing.T) {
	for _, mutation := range []string{"reset", "delete", "counter"} {
		t.Run(mutation, func(t *testing.T) {
			b, administrator := newAdministratorBrowser(t)
			key := newPasskey(t, 131)
			enrollAdministrator(t, b, administrator, key)
			pause := &pausedAdministrator{AdministratorPasskeys: administrator, lookedUp: make(chan struct{}), resume: make(chan struct{})}
			var release sync.Once
			resume := func() { release.Do(func() { close(pause.resume) }) }
			defer resume()
			b.m.administrator = pause
			guest := b.another()
			options := guest.begin("login")
			body := key.assertion(t, options, true, testOrigin, nil)
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- guest.request(http.MethodPost, "/api/donor/login/finish", body) }()
			select {
			case <-pause.lookedUp:
			case <-time.After(5 * time.Second):
				t.Fatal("real administrator lookup did not finish")
			}
			switch mutation {
			case "reset":
				if _, err := administrator.Reset(); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := b.m.db.Exec("DELETE FROM auth_credentials WHERE id=?", encodeURL(key.id)); err != nil {
					t.Fatal(err)
				}
			case "counter":
				adminOptions := beginAt(t, b, "/api/auth/login/begin", "")
				copy := *key
				copy.counter = 0
				readAdministratorStatus(t, b.request(http.MethodPost, "/api/auth/login/finish", copy.assertion(t, adminOptions, true, testOrigin, nil)))
			}
			resume()
			requireHTTP(t, <-result, http.StatusUnauthorized)
			for _, table := range []string{"donor_users", "donor_sessions", "donor_identity_links"} {
				var count int
				if err := b.m.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("stale proof committed %s: %d %v", table, count, err)
				}
			}
		})
	}
}

func TestClonedAdministratorDonorKeyUsesSharedQuarantine(t *testing.T) {
	b, administrator := newAdministratorBrowser(t)
	key, fallback := newPasskey(t, 141), newPasskey(t, 142)
	enrollAdministrator(t, b, administrator, key)
	enrollAdministrator(t, b, administrator, fallback)
	guest := b.another()
	linked := loginDonorKey(t, guest, key)
	clone := *key
	clone.counter = 0 // Same nonzero counter, with a genuine signature.
	attacker := b.another()
	options := attacker.begin("login")
	requireHTTP(t, attacker.request(http.MethodPost, "/api/donor/login/finish", clone.assertion(t, options, true, testOrigin, nil)), http.StatusUnauthorized)
	requireHTTP(t, b.request(http.MethodGet, "/protected", nil), http.StatusUnauthorized)
	current := guest.status(guest.request(http.MethodGet, "/api/donor/session", nil))
	if !current.Authenticated || current.DonorID != linked.DonorID || current.PasskeyCount != 1 {
		t.Fatal("quarantine altered ordinary identity or failed to remove cloned key")
	}
	if _, err := administrator.Password(); err == nil {
		t.Fatal("cloned passkey reopened password authentication")
	}
	var active bool
	if err := b.m.db.QueryRow("SELECT active FROM auth_credentials WHERE id=?", encodeURL(key.id)).Scan(&active); err != nil || active {
		t.Fatal("donor login did not quarantine shared administrator credential")
	}
	if recovered := loginDonorKey(t, b.another(), fallback); recovered.DonorID != linked.DonorID {
		t.Fatal("another valid administrator key could not recover ordinary identity")
	}
}

func TestRegistrationCannotCreateTwoAuthoritiesForOneCredentialID(t *testing.T) {
	b, administrator := newAdministratorBrowser(t)
	adminKey := newPasskey(t, 151)
	enrollAdministrator(t, b, administrator, adminKey)
	guest := b.another()
	options := guest.begin("register")
	duplicateDonor := newPasskey(t, 152)
	duplicateDonor.id = append([]byte{}, adminKey.id...)
	requireHTTP(t, guest.request(http.MethodPost, "/api/donor/register/finish", duplicateDonor.registration(t, options, true)), http.StatusUnauthorized)
	var count int
	if err := b.m.db.QueryRow("SELECT COUNT(*) FROM donor_users").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected cross-role credential committed an anonymous donor")
	}
	native := newPasskey(t, 153)
	guest.enroll(native)
	status := readAdministratorStatus(t, b.request(http.MethodGet, "/api/auth/status", nil))
	options = beginAt(t, b, "/api/auth/register/begin", status.CSRFToken)
	duplicateAdmin := newPasskey(t, 154)
	duplicateAdmin.id = append([]byte{}, native.id...)
	requireHTTP(t, b.request(http.MethodPost, "/api/auth/register/finish", duplicateAdmin.registration(t, options, true), map[string]string{"X-CSRF-Token": status.CSRFToken}), http.StatusConflict)
	if err := b.m.db.QueryRow("SELECT COUNT(*) FROM auth_credentials").Scan(&count); err != nil || count != 1 {
		t.Fatal("donor credential was enrolled with a second administrator authority")
	}
	requireHTTP(t, b.request(http.MethodGet, "/protected", nil), http.StatusNoContent)
	if !guest.status(guest.request(http.MethodGet, "/api/donor/session", nil)).Authenticated {
		t.Fatal("rejected collision damaged original donor session")
	}
}
