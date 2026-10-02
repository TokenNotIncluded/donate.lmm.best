package auth

import (
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/bcrypt"
)

type authStatus struct {
	Initialized   bool   `json:"initialized"`
	Authenticated bool   `json:"authenticated"`
	NeedsPasskey  bool   `json:"needs_passkey"`
	CSRFToken     string `json:"csrf_token"`
	PasskeyCount  int    `json:"passkey_count"`
}

func (m *Manager) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", m.status)
	mux.HandleFunc("POST /api/auth/password", m.post(m.password, 5))
	mux.HandleFunc("POST /api/auth/register/begin", m.post(m.registerBegin, 20))
	mux.HandleFunc("POST /api/auth/register/finish", m.post(m.registerFinish, 20))
	mux.HandleFunc("POST /api/auth/login/begin", m.post(m.loginBegin, 20))
	mux.HandleFunc("POST /api/auth/login/finish", m.post(m.loginFinish, 20))
	mux.HandleFunc("POST /api/auth/logout", m.post(m.logout, 30))
}

// Require grants access only to a full passkey-authenticated session. Mutations
// also need the exact public origin and the session's independent CSRF token.
func (m *Manager) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		s, err := m.lookupSession(r)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusUnauthorized, "请先使用 Passkey 登录")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "无法读取登录状态")
			return
		}
		if s.level != "admin" {
			fail(w, http.StatusForbidden, "绑定 Passkey 后才能进入管理后台")
			return
		}
		if !safeMethod(r.Method) && (!m.validOrigin(r) || !validCSRF(r, s)) {
			fail(w, http.StatusForbidden, "请求来源或 CSRF token 无效，请刷新后重试")
			return
		}
		next(w, r)
	}
}

func safeMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}

func (m *Manager) validOrigin(r *http.Request) bool {
	// Browsers send an origin without a trailing slash. Reject opaque origins,
	// multiple origins, credentials and paths instead of normalizing them away.
	raw := r.Header.Get("Origin")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme+"://"+strings.ToLower(u.Host) == m.origin
}

func validCSRF(r *http.Request, s session) bool {
	token := r.Header.Get("X-CSRF-Token")
	return len(token) == len(s.csrf) && s.csrf != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.csrf)) == 1
}

func (m *Manager) post(next http.HandlerFunc, limit int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !m.validOrigin(r) {
			fail(w, http.StatusForbidden, "请求来源无效")
			return
		}
		if !m.allowRequest(r, limit) {
			w.Header().Set("Retry-After", "60")
			fail(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			fail(w, http.StatusUnsupportedMediaType, "请求必须使用 application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		next(w, r)
	}
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
		for oldKey, old := range m.limits {
			if now.Sub(old.start) >= time.Minute {
				delete(m.limits, oldKey)
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

func (m *Manager) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s, err := m.lookupSession(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	m.writeStatus(w, r, s)
}

func (m *Manager) writeStatus(w http.ResponseWriter, r *http.Request, s session) {
	var enabled bool
	var count int
	err := m.db.QueryRowContext(r.Context(), `SELECT a.password_enabled,
 (SELECT COUNT(*) FROM auth_credentials c WHERE c.active=1 AND c.generation=a.generation)
FROM auth_state a WHERE a.id=1`).Scan(&enabled, &count)
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	respond(w, http.StatusOK, authStatus{Initialized: !enabled, Authenticated: s.level == "admin", NeedsPasskey: s.level == "bootstrap" && enabled, CSRFToken: s.csrf, PasskeyCount: count})
}

func (m *Manager) password(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Password) > 72 {
		fail(w, http.StatusBadRequest, "密码格式无效")
		return
	}
	s, err := m.readState(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法读取登录状态")
		return
	}
	if !s.passwordEnabled {
		fail(w, http.StatusForbidden, "已绑定 Passkey，密码登录已停用；丢失 Passkey 时只能通过命令行重置")
		return
	}
	if bcrypt.CompareHashAndPassword(s.passwordHash, []byte(input.Password)) != nil {
		fail(w, http.StatusUnauthorized, "密码错误")
		return
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法建立登录会话")
		return
	}
	defer tx.Rollback()
	if old, cookieErr := r.Cookie(m.sessionCookie); cookieErr == nil {
		if _, err = tx.Exec("DELETE FROM auth_sessions WHERE token_hash=?", tokenHash(old.Value)); err != nil {
			fail(w, http.StatusInternalServerError, "无法建立登录会话")
			return
		}
	}
	sess, token, err := m.newSession(tx, s.generation, "bootstrap")
	if err != nil {
		fail(w, http.StatusUnauthorized, "认证已重置，请重新登录")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "无法建立登录会话")
		return
	}
	m.setCookie(w, m.sessionCookie, token, time.Unix(sess.expires, 0))
	m.writeStatus(w, r, sess)
}

func (m *Manager) registrationSession(w http.ResponseWriter, r *http.Request) (session, bool) {
	s, err := m.lookupSession(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "请先登录后绑定 Passkey")
		return s, false
	}
	if !validCSRF(r, s) {
		fail(w, http.StatusForbidden, "CSRF token 无效，请刷新后重试")
		return s, false
	}
	return s, true
}

func (m *Manager) registerBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.registrationSession(w, r)
	if !ok {
		return
	}
	u, s, err := m.user(r.Context())
	if err != nil || s.generation != sess.generation {
		fail(w, http.StatusUnauthorized, "认证已重置，请重新登录")
		return
	}
	if len(u.credentials) >= 10 {
		fail(w, http.StatusConflict, "最多可绑定 10 个 Passkey")
		return
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
	if err = m.saveChallenge(w, r, "register", sess, s.generation, data); err != nil {
		fail(w, http.StatusUnauthorized, "认证已重置，请重新登录")
		return
	}
	respond(w, http.StatusOK, options)
}

func (m *Manager) registerFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.registrationSession(w, r)
	if !ok {
		return
	}
	c, err := m.consumeChallenge(w, r, "register", sess.hash)
	if err != nil {
		fail(w, http.StatusBadRequest, "绑定请求已失效，请重新开始")
		return
	}
	u, s, err := m.user(r.Context())
	if err != nil || s.generation != c.generation || s.generation != sess.generation {
		fail(w, http.StatusUnauthorized, "认证已重置，请重新登录")
		return
	}
	credential, err := m.wa.FinishRegistration(u, c.data, r)
	if err != nil {
		fail(w, http.StatusBadRequest, "Passkey 验证失败，请重新绑定（需要设备验证）")
		return
	}
	fullSession, token, err := m.acceptRegistration(r, sess, c.generation, credential)
	if err != nil {
		fail(w, http.StatusConflict, "绑定未完成，请重新登录后重试")
		return
	}
	m.setCookie(w, m.sessionCookie, token, time.Unix(fullSession.expires, 0))
	m.writeStatus(w, r, fullSession)
}

func (m *Manager) acceptRegistration(r *http.Request, sess session, generation int64, credential *webauthn.Credential) (session, string, error) {
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
	// Serialize cross-role credential enrollment before reading either owner.
	if _, err = tx.Exec("UPDATE auth_state SET generation=generation WHERE id=1"); err != nil {
		return session{}, "", err
	}
	var enabled bool
	var currentGeneration int64
	if err = tx.QueryRow("SELECT password_enabled,generation FROM auth_state WHERE id=1").Scan(&enabled, &currentGeneration); err != nil || currentGeneration != generation {
		return session{}, "", errStale
	}
	var valid bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE token_hash=? AND generation=? AND expires>? AND level=?)`, sess.hash, generation, time.Now().Unix(), sess.level).Scan(&valid); err != nil || !valid || (sess.level == "bootstrap" && !enabled) {
		return session{}, "", errStale
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM auth_credentials WHERE active=1 AND generation=?", generation).Scan(&count); err != nil {
		return session{}, "", err
	}
	if count >= 10 {
		return session{}, "", errors.New("too many passkeys")
	}
	if used, checkErr := donorCredentialIDInUse(r.Context(), tx, credential.ID); checkErr != nil || used {
		if checkErr != nil {
			return session{}, "", checkErr
		}
		return session{}, "", errStale
	}
	if _, err = tx.Exec("INSERT INTO auth_credentials(id,data,active,generation) VALUES(?,?,1,?)", base64.RawURLEncoding.EncodeToString(credential.ID), raw, generation); err != nil {
		return session{}, "", err
	}
	if _, err = tx.Exec("UPDATE auth_state SET password_enabled=0,password_hash=NULL WHERE id=1 AND generation=?", generation); err != nil {
		return session{}, "", err
	}
	if enabled {
		// The first enrollment revokes every other bootstrap session and pending
		// ceremony, so an old password session cannot enroll another credential.
		if _, err = tx.Exec("DELETE FROM auth_challenges"); err != nil {
			return session{}, "", err
		}
	}
	if _, err = tx.Exec("DELETE FROM auth_sessions WHERE level='bootstrap' OR token_hash=?", sess.hash); err != nil {
		return session{}, "", err
	}
	fullSession, token, err := m.newSession(tx, generation, "admin")
	if err != nil {
		return session{}, "", err
	}
	// File removal and CLI recovery both hold SQLite's writer lock. Removing
	// after COMMIT could race a recovery that has just created a new password.
	var previous []byte
	if enabled {
		previous, _ = os.ReadFile(m.bootstrapFile)
		if err = removeDisabledPassword(m.bootstrapFile); err != nil {
			return session{}, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		if len(previous) > 0 {
			_ = atomicPasswordFile(m.bootstrapFile, strings.TrimSpace(string(previous)))
		}
		return session{}, "", err
	}
	return fullSession, token, nil
}

func (m *Manager) loginBegin(w http.ResponseWriter, r *http.Request) {
	u, s, err := m.user(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法读取 Passkey")
		return
	}
	if s.passwordEnabled || len(u.credentials) == 0 {
		fail(w, http.StatusForbidden, "尚无可用 Passkey，请使用命令行获取初始密码或重置")
		return
	}
	options, data, err := m.wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(m.origin))
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法开始 Passkey 登录")
		return
	}
	if err = m.saveChallenge(w, r, "login", session{}, s.generation, data); err != nil {
		fail(w, http.StatusBadRequest, "认证已重置，请重新开始登录")
		return
	}
	respond(w, http.StatusOK, options)
}

func (m *Manager) loginFinish(w http.ResponseWriter, r *http.Request) {
	c, err := m.consumeChallenge(w, r, "login", "")
	if err != nil {
		fail(w, http.StatusUnauthorized, "登录请求已失效，请重新开始")
		return
	}
	u, s, err := m.user(r.Context())
	if err != nil || s.passwordEnabled || s.generation != c.generation {
		fail(w, http.StatusUnauthorized, "认证已重置，请重新登录")
		return
	}
	credential, err := m.wa.FinishLogin(u, c.data, r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Passkey 验证失败，请重新登录（需要设备验证）")
		return
	}
	if credential.Authenticator.CloneWarning {
		m.quarantineCredential(r, c.generation, u, credential)
		fail(w, http.StatusUnauthorized, "本次登录已拒绝，Passkey 计数异常；请使用其他 Passkey 或命令行重置")
		return
	}
	sess, token, err := m.acceptLogin(r, c.generation, u, credential)
	if err != nil {
		fail(w, http.StatusUnauthorized, "登录请求已失效，请重新开始")
		return
	}
	m.setCookie(w, m.sessionCookie, token, time.Unix(sess.expires, 0))
	m.writeStatus(w, r, sess)
}

func (m *Manager) acceptLogin(r *http.Request, generation int64, u adminUser, credential *webauthn.Credential) (session, string, error) {
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return session{}, "", err
	}
	defer tx.Rollback()
	if err = updateVerifiedCredential(r.Context(), tx, generation, u, credential); err != nil {
		return session{}, "", err
	}
	if old, cookieErr := r.Cookie(m.sessionCookie); cookieErr == nil {
		if _, err = tx.Exec("DELETE FROM auth_sessions WHERE token_hash=?", tokenHash(old.Value)); err != nil {
			return session{}, "", err
		}
	}
	sess, token, err := m.newSession(tx, generation, "admin")
	if err != nil {
		return session{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return session{}, "", err
	}
	return sess, token, nil
}

func (m *Manager) quarantineCredential(r *http.Request, generation int64, u adminUser, credential *webauthn.Credential) {
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if err = quarantineVerifiedCredential(r.Context(), tx, generation, u, credential); err != nil {
		return
	}
	_ = tx.Commit()
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	s, err := m.lookupSession(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "当前未登录")
		return
	}
	if !validCSRF(r, s) {
		fail(w, http.StatusForbidden, "CSRF token 无效，请刷新后重试")
		return
	}
	if _, err = m.db.ExecContext(r.Context(), "DELETE FROM auth_sessions WHERE token_hash=?", s.hash); err != nil {
		fail(w, http.StatusInternalServerError, "无法退出登录")
		return
	}
	_, _ = m.db.ExecContext(r.Context(), "DELETE FROM auth_challenges WHERE session_hash=?", s.hash)
	m.clearCookie(w, m.sessionCookie)
	m.clearCookie(w, m.flowCookie)
	w.WriteHeader(http.StatusNoContent)
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
