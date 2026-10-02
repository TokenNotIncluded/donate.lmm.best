package donors

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type sessionStatus struct {
	Authenticated bool   `json:"authenticated"`
	User          *User  `json:"user"`
	DonorID       string `json:"donor_id"`
	CSRFToken     string `json:"csrf_token"`
	PasskeyCount  int    `json:"passkey_count"`
}

func respond(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, code int, message string) {
	respond(w, code, map[string]string{"error": message})
}
func authFailed(w http.ResponseWriter) {
	fail(w, http.StatusUnauthorized, "Passkey 验证失败，请重试")
}

func (m *Manager) checkPost(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "需要 POST 请求")
		return false
	}
	if !m.CheckOrigin(r) {
		fail(w, http.StatusForbidden, "请求来源无效")
		return false
	}
	if !m.allowRequest(r, 20) {
		w.Header().Set("Retry-After", "60")
		fail(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "需要 application/json 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	return true
}

func (m *Manager) allowRequest(r *http.Request, maximum int) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	key := r.URL.Path + ":" + host
	now := time.Now()
	m.limitMu.Lock()
	defer m.limitMu.Unlock()
	entry, exists := m.limits[key]
	if now.Sub(entry.start) >= time.Minute {
		entry = rateWindow{start: now}
	}
	if len(m.limits) >= 4096 {
		for k, v := range m.limits {
			if now.Sub(v.start) >= time.Minute {
				delete(m.limits, k)
			}
		}
		if len(m.limits) >= 4096 && !exists {
			return false
		}
	}
	entry.count++
	m.limits[key] = entry
	return entry.count <= maximum
}

// Session reports only the independently authenticated donor's own identity.
func (m *Manager) Session(w http.ResponseWriter, r *http.Request) {
	s, err := m.lookupSession(r)
	if errors.Is(err, sql.ErrNoRows) {
		// Clear a stale cookie so the following anonymous donation is explicit.
		if _, cookieErr := r.Cookie(sessionCookie); cookieErr == nil {
			m.clearCookie(w, sessionCookie)
		}
		respond(w, http.StatusOK, sessionStatus{})
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	m.writeSession(w, r, s)
}

func (m *Manager) writeSession(w http.ResponseWriter, r *http.Request, s session) {
	count, err := m.passkeyCount(r.Context(), s.user.ID, nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	respond(w, http.StatusOK, sessionStatus{Authenticated: true, User: &s.user, DonorID: s.user.ID, CSRFToken: s.csrf, PasskeyCount: count})
}

// RegisterBegin starts either a new anonymous identity or a backup credential
// for the currently authenticated donor. It never takes a client-supplied ID.
func (m *Manager) RegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !m.checkPost(w, r) {
		return
	}
	s, err := m.lookupSession(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	var u passkeyUser
	if err == nil {
		if !validCSRF(r, s) {
			authFailed(w)
			return
		}
		u, err = m.loadUser(r.Context(), s.user.ID)
		if err != nil {
			authFailed(w)
			return
		}
		count, countErr := m.passkeyCount(r.Context(), u.ID, nil)
		if countErr != nil {
			fail(w, http.StatusInternalServerError, "无法读取 Passkey")
			return
		}
		if count >= maxPasskeys {
			fail(w, http.StatusConflict, "最多绑定 10 个 Passkey")
			return
		}
	} else {
		if _, cookieErr := r.Cookie(sessionCookie); cookieErr == nil {
			authFailed(w)
			return
		}
		id, randomErr := randomToken()
		if randomErr != nil {
			fail(w, http.StatusInternalServerError, "无法开始 Passkey 绑定")
			return
		}
		handle, _ := base64.RawURLEncoding.DecodeString(id)
		u = passkeyUser{User: User{ID: id}, handle: handle}
	}
	exclusions := make([]protocol.CredentialDescriptor, 0, len(u.credentials))
	for _, credential := range u.credentials {
		exclusions = append(exclusions, credential.Descriptor())
	}
	options, data, err := m.wa.BeginRegistration(u, webauthn.WithExclusions(exclusions), webauthn.WithRegistrationOrigin(m.origin))
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法开始 Passkey 绑定")
		return
	}
	if err = m.saveChallenge(w, r, "register", u.ID, s.hash, data); err != nil {
		fail(w, http.StatusInternalServerError, "无法开始 Passkey 绑定")
		return
	}
	respond(w, http.StatusOK, options)
}

func (m *Manager) requestSession(r *http.Request) (session, error) {
	s, err := m.lookupSession(r)
	if errors.Is(err, sql.ErrNoRows) {
		if _, cookieErr := r.Cookie(sessionCookie); errors.Is(cookieErr, http.ErrNoCookie) {
			return session{}, nil
		}
		return session{}, ErrUnauthorized
	}
	return s, err
}

// RegisterFinish verifies the actual WebAuthn attestation before atomically
// creating an identity and issuing its first independently scoped session.
func (m *Manager) RegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !m.checkPost(w, r) {
		return
	}
	s, sessionErr := m.requestSession(r)
	c, err := m.consumeChallenge(w, r, "register", s.hash)
	if err != nil || sessionErr != nil {
		authFailed(w)
		return
	}
	handle, err := base64.RawURLEncoding.DecodeString(c.userID)
	if err != nil || len(handle) != 32 || !bytes.Equal(handle, c.data.UserID) {
		authFailed(w)
		return
	}
	u := passkeyUser{User: User{ID: c.userID}, handle: handle}
	if s.hash != "" {
		if c.userID != s.user.ID || !validCSRF(r, s) {
			authFailed(w)
			return
		}
		u, err = m.loadUser(r.Context(), c.userID)
		if err != nil {
			authFailed(w)
			return
		}
	}
	credential, err := m.wa.FinishRegistration(u, c.data, r)
	if err != nil || !credential.Flags.UserVerified {
		authFailed(w)
		return
	}
	full, token, err := m.acceptRegistration(r, s, u.User, credential)
	if err != nil {
		authFailed(w)
		return
	}
	m.setCookie(w, sessionCookie, token, time.Unix(full.expires, 0))
	m.writeSession(w, r, full)
}

func (m *Manager) acceptRegistration(r *http.Request, previous session, user User, credential *webauthn.Credential) (session, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := json.Marshal(credential)
	if err != nil {
		return session{}, "", err
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return session{}, "", err
	}
	defer tx.Rollback()
	if previous.hash == "" {
		user.CreatedAt = time.Now().Unix()
		if _, err = tx.Exec("INSERT INTO donor_users(id,display_name,created_at) VALUES(?,?,?)", user.ID, user.DisplayName, user.CreatedAt); err != nil {
			return session{}, "", err
		}
	} else {
		// The write acquires SQLite's writer lock before checking this session,
		// including sessions revoked concurrently by another server process.
		if _, err = tx.Exec("UPDATE donor_users SET created_at=created_at WHERE id=?", user.ID); err != nil {
			return session{}, "", err
		}
		var valid bool
		if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM donor_sessions WHERE token_hash=? AND user_id=? AND expires>?)", previous.hash, user.ID, time.Now().Unix()).Scan(&valid); err != nil || !valid {
			return session{}, "", ErrUnauthorized
		}
	}
	count, err := m.passkeyCount(r.Context(), user.ID, tx)
	if err != nil {
		return session{}, "", err
	}
	if count >= maxPasskeys {
		return session{}, "", ErrUnauthorized
	}
	if m.administrator != nil {
		used, checkErr := m.administrator.DonorPasskeyIDInUse(r.Context(), tx, credential.ID)
		if checkErr != nil {
			return session{}, "", checkErr
		}
		if used {
			return session{}, "", ErrUnauthorized
		}
	}
	if _, err = tx.Exec("INSERT INTO donor_credentials(id,user_id,data) VALUES(?,?,?)", base64.RawURLEncoding.EncodeToString(credential.ID), user.ID, raw); err != nil {
		return session{}, "", err
	}
	full, token, err := m.newSession(tx, user, previous.hash)
	if err != nil {
		return full, "", err
	}
	if err = tx.Commit(); err != nil {
		return full, "", err
	}
	return full, token, nil
}

// LoginBegin requests a discoverable passkey. No identifier supplied by the
// browser can select a donor or disclose whether an account exists.
func (m *Manager) LoginBegin(w http.ResponseWriter, r *http.Request) {
	if !m.checkPost(w, r) {
		return
	}
	s, err := m.requestSession(r)
	if err != nil {
		authFailed(w)
		return
	}
	if s.hash != "" && !validCSRF(r, s) {
		authFailed(w)
		return
	}
	options, data, err := m.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(m.origin))
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法开始 Passkey 登录")
		return
	}
	if err = m.saveChallenge(w, r, "login", "", s.hash, data); err != nil {
		fail(w, http.StatusInternalServerError, "无法开始 Passkey 登录")
		return
	}
	respond(w, http.StatusOK, options)
}

func (m *Manager) LoginFinish(w http.ResponseWriter, r *http.Request) {
	if !m.checkPost(w, r) {
		return
	}
	s, sessionErr := m.requestSession(r)
	c, err := m.consumeChallenge(w, r, "login", s.hash)
	if err != nil || sessionErr != nil || (s.hash != "" && !validCSRF(r, s)) {
		authFailed(w)
		return
	}
	bridged := false
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(userHandle) != 32 {
			return nil, ErrUnauthorized
		}
		id := base64.RawURLEncoding.EncodeToString(userHandle)
		var owner string
		err := m.db.QueryRowContext(r.Context(), "SELECT user_id FROM donor_credentials WHERE id=?", base64.RawURLEncoding.EncodeToString(rawID)).Scan(&owner)
		if err == nil && owner == id {
			return m.loadUser(r.Context(), id)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if m.administrator != nil {
			principal, lookupErr := m.administrator.LookupDonorPasskey(r.Context(), rawID, userHandle)
			if lookupErr == nil {
				bridged = true
			}
			return principal, lookupErr
		}
		return nil, ErrUnauthorized
	}
	u, credential, err := m.wa.FinishPasskeyLogin(handler, c.data, r)
	if err != nil || !credential.Flags.UserVerified {
		authFailed(w)
		return
	}
	if credential.Authenticator.CloneWarning {
		if bridged {
			_ = m.administrator.QuarantineDonorPasskey(r.Context(), u, credential)
		}
		authFailed(w)
		return
	}
	var full session
	var token string
	if bridged {
		full, token, err = m.acceptAdministratorLogin(r, s, u, credential)
	} else {
		user, ok := u.(passkeyUser)
		if !ok {
			authFailed(w)
			return
		}
		full, token, err = m.acceptLogin(r, s, user.User, credential)
	}
	if err != nil {
		authFailed(w)
		return
	}
	m.setCookie(w, sessionCookie, token, time.Unix(full.expires, 0))
	m.writeSession(w, r, full)
}

func (m *Manager) acceptLogin(r *http.Request, previous session, user User, credential *webauthn.Credential) (session, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := json.Marshal(credential)
	if err != nil {
		return session{}, "", err
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return session{}, "", err
	}
	defer tx.Rollback()
	// Updating the credential first obtains the writer lock. Rechecking its
	// stored counter prevents two separately verified assertions from racing.
	var storedRaw []byte
	id := base64.RawURLEncoding.EncodeToString(credential.ID)
	if _, err = tx.Exec("UPDATE donor_credentials SET data=data WHERE id=? AND user_id=?", id, user.ID); err != nil {
		return session{}, "", err
	}
	if err = tx.QueryRow("SELECT data FROM donor_credentials WHERE id=? AND user_id=?", id, user.ID).Scan(&storedRaw); err != nil {
		return session{}, "", err
	}
	var stored webauthn.Credential
	if err = json.Unmarshal(storedRaw, &stored); err != nil {
		return session{}, "", err
	}
	if (stored.Authenticator.SignCount != 0 || credential.Authenticator.SignCount != 0) && credential.Authenticator.SignCount <= stored.Authenticator.SignCount {
		return session{}, "", ErrUnauthorized
	}
	if previous.hash != "" {
		var valid bool
		if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM donor_sessions WHERE token_hash=? AND expires>?)", previous.hash, time.Now().Unix()).Scan(&valid); err != nil || !valid {
			return session{}, "", ErrUnauthorized
		}
	}
	if _, err = tx.Exec("UPDATE donor_credentials SET data=? WHERE id=? AND user_id=?", raw, id, user.ID); err != nil {
		return session{}, "", err
	}
	full, token, err := m.newSession(tx, user, previous.hash)
	if err != nil {
		return full, "", err
	}
	if err = tx.Commit(); err != nil {
		return full, "", err
	}
	return full, token, nil
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if !m.checkPost(w, r) {
		return
	}
	s, err := m.lookupSession(r)
	if err != nil || !validCSRF(r, s) {
		authFailed(w)
		return
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, "退出失败")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM donor_sessions WHERE token_hash=?", s.hash); err != nil {
		fail(w, http.StatusInternalServerError, "退出失败")
		return
	}
	if _, err = tx.Exec("DELETE FROM donor_challenges WHERE session_hash=?", s.hash); err != nil {
		fail(w, http.StatusInternalServerError, "退出失败")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "退出失败")
		return
	}
	m.clearCookie(w, sessionCookie)
	m.clearCookie(w, flowCookie)
	respond(w, http.StatusOK, sessionStatus{})
}
