// Package donors owns optional donor identities. It shares neither credentials
// nor authorization with the administrator's independent auth package.
package donors

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	sessionCookie     = "donate_donor"
	flowCookie        = "donate_donor_challenge"
	sessionLifetime   = 30 * 24 * time.Hour
	challengeLifetime = 5 * time.Minute
	maxPasskeys       = 10
)

var ErrUnauthorized = errors.New("donor authentication required")

// User.ID is an opaque, stable identity for associating donations and future
// donor benefits. Public sessions expose no credential or authentication data.
type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   int64  `json:"created_at"`
}

type Manager struct {
	db      *sql.DB
	wa      *webauthn.WebAuthn
	origin  string
	secure  bool
	mu      sync.Mutex
	limitMu sync.Mutex
	limits  map[string]rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}
type session struct {
	hash, csrf string
	user       User
	expires    int64
}
type challenge struct {
	data                      webauthn.SessionData
	userID, sessionHash, kind string
	expires                   int64
}
type passkeyUser struct {
	User
	handle      []byte
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte   { return u.handle }
func (u passkeyUser) WebAuthnName() string { return "donor-" + u.ID[:10] }
func (u passkeyUser) WebAuthnDisplayName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return "Donate"
}
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// New validates the exact public origin and creates independent donor tables.
// No donor records are created before a verified WebAuthn registration.
func New(db *sql.DB, publicURL string) (*Manager, error) {
	if db == nil {
		return nil, errors.New("donor database is required")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public URL must be an HTTP(S) origin")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || (u.Scheme == "http" && host != "localhost") {
		return nil, errors.New("passkeys require HTTPS and a domain; HTTP localhost is allowed for development")
	}
	origin := canonicalOrigin(u)
	wa, err := webauthn.New(&webauthn.Config{
		RPID: host, RPDisplayName: "Donate", RPOrigins: []string{origin},
		AttestationPreference:  protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{RequireResidentKey: protocol.ResidentKeyRequired(), ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: challengeLifetime},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: challengeLifetime},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure donor passkeys: %w", err)
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS donor_users (
 id TEXT PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS donor_credentials (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES donor_users(id), data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS donor_credentials_user ON donor_credentials(user_id);
CREATE TABLE IF NOT EXISTS donor_sessions (
 token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES donor_users(id),
 csrf TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS donor_sessions_expires ON donor_sessions(expires);
CREATE TABLE IF NOT EXISTS donor_challenges (
 token_hash TEXT PRIMARY KEY, kind TEXT NOT NULL, user_id TEXT NOT NULL,
 session_hash TEXT NOT NULL, data BLOB NOT NULL, expires INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS donor_challenges_expires ON donor_challenges(expires);`)
	if err != nil {
		return nil, fmt.Errorf("create donor authentication tables: %w", err)
	}
	return &Manager{db: db, wa: wa, origin: origin, secure: u.Scheme == "https", limits: make(map[string]rateWindow)}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func canonicalOrigin(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80" {
		host = strings.ToLower(u.Hostname())
	}
	return u.Scheme + "://" + host
}

// CheckOrigin accepts only the configured, non-opaque origin without a path.
func (m *Manager) CheckOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && canonicalOrigin(u) == m.origin
}

func (m *Manager) lookupSession(r *http.Request) (session, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 43 {
		return session{}, sql.ErrNoRows
	}
	s := session{hash: tokenHash(c.Value)}
	err = m.db.QueryRowContext(r.Context(), `SELECT s.csrf,s.expires,u.id,u.display_name,u.created_at
FROM donor_sessions s JOIN donor_users u ON u.id=s.user_id
WHERE s.token_hash=? AND s.expires>?`, s.hash, time.Now().Unix()).Scan(&s.csrf, &s.expires, &s.user.ID, &s.user.DisplayName, &s.user.CreatedAt)
	return s, err
}

// Current returns nil for an anonymous request. A stale donor cookie is an
// authorization error so a pending donation cannot silently lose its identity.
// Only donor cookies are inspected; an administrator never identifies a donor.
func (m *Manager) Current(r *http.Request) (*User, error) {
	if _, err := r.Cookie(sessionCookie); errors.Is(err, http.ErrNoCookie) {
		return nil, nil
	}
	s, err := m.lookupSession(r)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	return &s.user, nil
}

func validCSRF(r *http.Request, s session) bool {
	token := r.Header.Get("X-CSRF-Token")
	return s.csrf != "" && len(token) == len(s.csrf) && subtle.ConstantTimeCompare([]byte(token), []byte(s.csrf)) == 1
}

// Authorize checks a donor mutation's identity, exact origin, and independent
// CSRF token. Its identity grants no administrator authorization.
func (m *Manager) Authorize(r *http.Request) (*User, error) {
	s, err := m.lookupSession(r)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if !m.CheckOrigin(r) || !validCSRF(r, s) {
		return nil, ErrUnauthorized
	}
	return &s.user, nil
}

func (m *Manager) loadUser(ctx context.Context, id string) (passkeyUser, error) {
	handle, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(handle) != 32 {
		return passkeyUser{}, ErrUnauthorized
	}
	u := passkeyUser{handle: handle}
	if err = m.db.QueryRowContext(ctx, "SELECT id,display_name,created_at FROM donor_users WHERE id=?", id).Scan(&u.ID, &u.DisplayName, &u.CreatedAt); err != nil {
		return u, err
	}
	rows, err := m.db.QueryContext(ctx, "SELECT data FROM donor_credentials WHERE user_id=? ORDER BY id", id)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var credential webauthn.Credential
		if err = rows.Scan(&raw); err != nil {
			return u, err
		}
		if err = json.Unmarshal(raw, &credential); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, credential)
	}
	return u, rows.Err()
}

func (m *Manager) saveChallenge(w http.ResponseWriter, r *http.Request, kind, userID, sessionHash string, data *webauthn.SessionData) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if old, err := r.Cookie(flowCookie); err == nil {
		if _, err = tx.Exec("DELETE FROM donor_challenges WHERE token_hash=?", tokenHash(old.Value)); err != nil {
			return err
		}
	}
	for _, table := range []string{"donor_challenges", "donor_sessions"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE expires<=?", time.Now().Unix()); err != nil {
			return err
		}
	}
	if sessionHash != "" {
		var valid bool
		if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM donor_sessions WHERE token_hash=? AND expires>?)", sessionHash, time.Now().Unix()).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrUnauthorized
		}
	}
	expires := time.Now().Add(challengeLifetime)
	if _, err = tx.Exec("INSERT INTO donor_challenges(token_hash,kind,user_id,session_hash,data,expires) VALUES(?,?,?,?,?,?)", tokenHash(token), kind, userID, sessionHash, raw, expires.Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	m.setCookie(w, flowCookie, token, expires)
	return nil
}

// DELETE RETURNING atomically consumes even an invalid attempt, including
// parallel submissions; no in-memory challenge survives a restart or replay.
func (m *Manager) consumeChallenge(w http.ResponseWriter, r *http.Request, kind, sessionHash string) (challenge, error) {
	m.clearCookie(w, flowCookie)
	cookie, err := r.Cookie(flowCookie)
	if err != nil || len(cookie.Value) != 43 {
		return challenge{}, ErrUnauthorized
	}
	var raw []byte
	var c challenge
	err = m.db.QueryRowContext(r.Context(), `DELETE FROM donor_challenges WHERE token_hash=?
RETURNING data,kind,user_id,session_hash,expires`, tokenHash(cookie.Value)).Scan(&raw, &c.kind, &c.userID, &c.sessionHash, &c.expires)
	if err != nil || c.kind != kind || c.sessionHash != sessionHash || c.expires <= time.Now().Unix() {
		return c, ErrUnauthorized
	}
	if err = json.Unmarshal(raw, &c.data); err != nil {
		return c, err
	}
	return c, nil
}

func (m *Manager) newSession(tx *sql.Tx, user User, oldHash string) (session, string, error) {
	token, err := randomToken()
	if err != nil {
		return session{}, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return session{}, "", err
	}
	s := session{hash: tokenHash(token), csrf: csrf, user: user, expires: time.Now().Add(sessionLifetime).Unix()}
	if oldHash != "" {
		if _, err = tx.Exec("DELETE FROM donor_sessions WHERE token_hash=?", oldHash); err != nil {
			return s, "", err
		}
		if _, err = tx.Exec("DELETE FROM donor_challenges WHERE session_hash=?", oldHash); err != nil {
			return s, "", err
		}
	}
	_, err = tx.Exec("INSERT INTO donor_sessions(token_hash,user_id,csrf,expires) VALUES(?,?,?,?)", s.hash, user.ID, csrf, s.expires)
	return s, token, err
}

func (m *Manager) setCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: m.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}
func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", Secure: m.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
