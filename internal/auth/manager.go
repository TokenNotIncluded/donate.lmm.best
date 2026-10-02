// Package auth owns the single administrator's password bootstrap and passkeys.
// Password sessions can only enroll a passkey. Administrator sessions are issued
// only after a verified WebAuthn ceremony with user verification.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/bcrypt"
)

const (
	bootstrapLifetime = 10 * time.Minute
	adminLifetime     = 8 * time.Hour
	challengeLifetime = 5 * time.Minute
)

var errStale = errors.New("authentication was reset or expired; start again")

type Manager struct {
	db            *sql.DB
	wa            *webauthn.WebAuthn
	origin        string
	secure        bool
	bootstrapFile string
	sessionCookie string
	flowCookie    string
	mu            sync.Mutex
	limitMu       sync.Mutex
	limits        map[string]rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

type adminUser struct {
	id          []byte
	credentials []webauthn.Credential
}

func (u adminUser) WebAuthnID() []byte                         { return u.id }
func (u adminUser) WebAuthnName() string                       { return "admin" }
func (u adminUser) WebAuthnDisplayName() string                { return "Donation administrator" }
func (u adminUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

type state struct {
	userID          []byte
	passwordHash    []byte
	passwordEnabled bool
	generation      int64
}

type session struct {
	hash, csrf, level string
	expires           int64
	generation        int64
}

type challenge struct {
	data       webauthn.SessionData
	session    string
	generation int64
}

// New validates the public origin, creates the auth tables, and creates a
// random bootstrap password on the first startup. It never prints passwords.
func New(db *sql.DB, dataDir, publicURL string) (*Manager, error) {
	if db == nil {
		return nil, errors.New("auth database is required")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public URL must be an HTTP(S) origin without a path, query, or user information")
	}
	hostname := strings.ToLower(u.Hostname())
	if net.ParseIP(hostname) != nil {
		return nil, errors.New("passkeys require a domain name; use http://localhost:8080 for local development")
	}
	if u.Scheme == "http" && hostname != "localhost" {
		return nil, errors.New("public URL must use HTTPS (HTTP is allowed only for localhost development)")
	}
	origin := u.Scheme + "://" + strings.ToLower(u.Host)
	wa, err := webauthn.New(&webauthn.Config{
		RPID: hostname, RPDisplayName: "留一点燃料", RPOrigins: []string{origin},
		AttestationPreference:  protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{RequireResidentKey: protocol.ResidentKeyRequired(), ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: challengeLifetime},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: challengeLifetime},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure passkeys: %w", err)
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dataDir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{db: db, wa: wa, origin: origin, secure: u.Scheme == "https", bootstrapFile: filepath.Join(dataDir, "bootstrap-password"), limits: make(map[string]rateWindow), sessionCookie: "donate_session", flowCookie: "donate_challenge"}
	if m.secure {
		m.sessionCookie, m.flowCookie = "__Host-donate_session", "__Host-donate_challenge"
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS auth_state (
 id INTEGER PRIMARY KEY CHECK (id = 1), user_id BLOB NOT NULL,
 password_hash BLOB, password_enabled INTEGER NOT NULL, generation INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_credentials (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, active INTEGER NOT NULL DEFAULT 1, generation INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY, csrf TEXT NOT NULL, level TEXT NOT NULL,
 expires INTEGER NOT NULL, generation INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_challenges (
 token_hash TEXT PRIMARY KEY, kind TEXT NOT NULL, session_hash TEXT NOT NULL,
 data BLOB NOT NULL, expires INTEGER NOT NULL, generation INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_sessions_expires ON auth_sessions(expires);
CREATE INDEX IF NOT EXISTS auth_challenges_expires ON auth_challenges(expires);`)
	if err != nil {
		return nil, fmt.Errorf("create authentication tables: %w", err)
	}
	if err = m.initialize(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) initialize() error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire SQLite's writer lock before inspecting the bootstrap state. CLI
	// recovery can run in a separate process while the HTTP server is running.
	if _, err = tx.Exec("UPDATE auth_state SET generation=generation WHERE id=1"); err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM auth_state WHERE id=1").Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		var enabled bool
		if err = tx.QueryRow("SELECT password_enabled FROM auth_state WHERE id=1").Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			if err = os.Remove(m.bootstrapFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove disabled bootstrap password: %w", err)
			}
		}
		return tx.Commit()
	}
	password, err := randomToken()
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	userID := make([]byte, 32)
	if _, err = rand.Read(userID); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO auth_state(id,user_id,password_hash,password_enabled,generation) VALUES(1,?,?,1,1)", userID, hash); err != nil {
		return err
	}
	if err = atomicPasswordFile(m.bootstrapFile, password); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	return nil
}

// Password returns the bootstrap secret for CLI use only. Bound passkeys disable
// this path, even if an obsolete password file survives a filesystem failure.
func (m *Manager) Password() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.readState(context.Background())
	if err != nil {
		return "", err
	}
	if !s.passwordEnabled {
		return "", errors.New("password login is disabled; use a passkey or run 'donate admin reset'")
	}
	info, err := os.Lstat(m.bootstrapFile)
	if err != nil {
		return "", errors.New("bootstrap password is unavailable; run 'donate admin reset'")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("bootstrap password file must be a regular private file (mode 0600)")
	}
	b, err := os.ReadFile(m.bootstrapFile)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(b))
	if bcrypt.CompareHashAndPassword(s.passwordHash, []byte(password)) != nil {
		return "", errors.New("bootstrap password file does not match database; run 'donate admin reset'")
	}
	return password, nil
}

// Reset is a local CLI recovery action. It invalidates every credential,
// session, and in-flight ceremony, rotates the bootstrap secret and user ID,
// and reopens password bootstrap until a new passkey is enrolled.
func (m *Manager) Reset() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	password, err := randomToken()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	userID := make([]byte, 32)
	if _, err = rand.Read(userID); err != nil {
		return "", err
	}
	tx, err := m.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE auth_state SET user_id=?,password_hash=?,password_enabled=1,generation=generation+1 WHERE id=1", userID, hash); err != nil {
		return "", err
	}
	for _, table := range []string{"auth_credentials", "auth_sessions", "auth_challenges"} {
		if _, err = tx.Exec("DELETE FROM " + table); err != nil {
			return "", err
		}
	}
	previous, readErr := os.ReadFile(m.bootstrapFile)
	if err = atomicPasswordFile(m.bootstrapFile, password); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		if readErr == nil {
			_ = atomicPasswordFile(m.bootstrapFile, strings.TrimSpace(string(previous)))
		} else {
			_ = os.Remove(m.bootstrapFile)
		}
		return "", fmt.Errorf("reset authentication: %w", err)
	}
	return password, nil
}

func atomicPasswordFile(path, password string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".bootstrap-password-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(password + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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

func (m *Manager) readState(ctx context.Context) (s state, err error) {
	err = m.db.QueryRowContext(ctx, "SELECT user_id,password_hash,password_enabled,generation FROM auth_state WHERE id=1").Scan(&s.userID, &s.passwordHash, &s.passwordEnabled, &s.generation)
	return
}

func (m *Manager) user(ctx context.Context) (adminUser, state, error) {
	s, err := m.readState(ctx)
	if err != nil {
		return adminUser{}, s, err
	}
	u := adminUser{id: s.userID}
	rows, err := m.db.QueryContext(ctx, "SELECT data FROM auth_credentials WHERE active=1 AND generation=? ORDER BY id", s.generation)
	if err != nil {
		return u, s, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var credential webauthn.Credential
		if err = rows.Scan(&raw); err != nil {
			return u, s, err
		}
		if err = json.Unmarshal(raw, &credential); err != nil {
			return u, s, err
		}
		u.credentials = append(u.credentials, credential)
	}
	return u, s, rows.Err()
}

func (m *Manager) lookupSession(r *http.Request) (session, error) {
	cookie, err := r.Cookie(m.sessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return session{}, sql.ErrNoRows
	}
	s := session{hash: tokenHash(cookie.Value)}
	err = m.db.QueryRowContext(r.Context(), `SELECT s.csrf,s.level,s.expires,s.generation FROM auth_sessions s
JOIN auth_state a ON a.id=1 AND a.generation=s.generation
WHERE s.token_hash=? AND s.expires>? AND (s.level='admin' OR a.password_enabled=1)`, s.hash, time.Now().Unix()).Scan(&s.csrf, &s.level, &s.expires, &s.generation)
	return s, err
}

func (m *Manager) newSession(tx *sql.Tx, generation int64, level string) (session, string, error) {
	token, err := randomToken()
	if err != nil {
		return session{}, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return session{}, "", err
	}
	lifetime := adminLifetime
	if level == "bootstrap" {
		lifetime = bootstrapLifetime
	}
	s := session{hash: tokenHash(token), csrf: csrf, level: level, expires: time.Now().Add(lifetime).Unix(), generation: generation}
	result, err := tx.Exec(`INSERT INTO auth_sessions(token_hash,csrf,level,expires,generation)
SELECT ?,?,?,?,generation FROM auth_state WHERE id=1 AND generation=? AND (?='admin' OR password_enabled=1)`, s.hash, s.csrf, level, s.expires, generation, level)
	if err != nil {
		return s, "", err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return s, "", errStale
	}
	return s, token, nil
}

func (m *Manager) saveChallenge(w http.ResponseWriter, r *http.Request, kind string, s session, generation int64, data *webauthn.SessionData) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if old, cookieErr := r.Cookie(m.flowCookie); cookieErr == nil {
		_, _ = m.db.ExecContext(r.Context(), "DELETE FROM auth_challenges WHERE token_hash=?", tokenHash(old.Value))
	}
	_, _ = m.db.ExecContext(r.Context(), "DELETE FROM auth_challenges WHERE expires<=?", time.Now().Unix())
	_, _ = m.db.ExecContext(r.Context(), "DELETE FROM auth_sessions WHERE expires<=?", time.Now().Unix())
	result, err := m.db.ExecContext(r.Context(), `INSERT INTO auth_challenges(token_hash,kind,session_hash,data,expires,generation)
SELECT ?,?,?,?,?,generation FROM auth_state WHERE id=1 AND generation=?
AND (?='login' AND password_enabled=0 OR ?='register' AND EXISTS
 (SELECT 1 FROM auth_sessions WHERE token_hash=? AND generation=? AND expires>?))`, tokenHash(token), kind, s.hash, raw, time.Now().Add(challengeLifetime).Unix(), generation, kind, kind, s.hash, generation, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errStale
	}
	m.setCookie(w, m.flowCookie, token, time.Now().Add(challengeLifetime))
	return nil
}

// DELETE RETURNING is deliberately used here: even invalid finish submissions
// consume their challenge, and simultaneous requests cannot reuse a ceremony.
func (m *Manager) consumeChallenge(w http.ResponseWriter, r *http.Request, kind, sessionHash string) (challenge, error) {
	m.clearCookie(w, m.flowCookie)
	cookie, err := r.Cookie(m.flowCookie)
	if err != nil || len(cookie.Value) != 43 {
		return challenge{}, errStale
	}
	var raw []byte
	var expires int64
	var storedKind string
	var c challenge
	err = m.db.QueryRowContext(r.Context(), `DELETE FROM auth_challenges WHERE token_hash=?
RETURNING data,expires,session_hash,generation,kind`, tokenHash(cookie.Value)).Scan(&raw, &expires, &c.session, &c.generation, &storedKind)
	if err != nil {
		return c, errStale
	}
	if expires <= time.Now().Unix() || c.session != sessionHash || storedKind != kind {
		return c, errStale
	}
	if err = json.Unmarshal(raw, &c.data); err != nil {
		return c, err
	}
	return c, nil
}

func (m *Manager) setCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: m.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", Secure: m.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
