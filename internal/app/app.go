package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/auth"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/chain"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/donors"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
	_ "modernc.org/sqlite"
)

type App struct {
	Chain              *chain.Service
	cryptoWake         chan struct{}
	cryptoHub          receiptHub
	starMu             sync.Mutex
	starCache          starSnapshot
	DB                 *sql.DB
	Auth               *auth.Manager
	Donors             *donors.Manager
	Notify             *notify.Service
	Payments           *payments.Service
	DataDir, PublicURL string
	assets             fs.FS
	checkoutMu         sync.Mutex
	catalogMu          sync.Mutex
	limitsMu           sync.Mutex
	limits             map[string]limit
	trustedProxies     []*net.IPNet
	keysMu             sync.Mutex
	keys               map[string]*keyLock
}
type keyLock struct {
	mu   sync.Mutex
	refs int
}
type limit struct {
	Count int
	Until time.Time
}

func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func validID(s string) bool { return idPattern.MatchString(s) }

// Browsers serialize an origin without the scheme's default port. Preserve
// explicit non-default ports, since they identify a different security origin.
func normalizedPublicOrigin(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80" {
		host = strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return (&url.URL{Scheme: u.Scheme, Host: host}).String()
}

func New(dataDir, publicURL string, assets fs.FS) (*App, error) {
	u, e := url.Parse(publicURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("DONATE_PUBLIC_URL must be a single site origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "localhost") {
		return nil, errors.New("use HTTPS in production, or http://localhost for local development")
	}
	if e = os.MkdirAll(dataDir, 0700); e != nil {
		return nil, e
	}
	if e = os.Chmod(dataDir, 0700); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(dataDir, "uploads"), 0700); e != nil {
		return nil, e
	}
	dbPath := filepath.Join(dataDir, "donate.sqlite")
	f, e := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	if e = os.Chmod(dbPath, 0600); e != nil {
		return nil, e
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath}).String() + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	a := &App{DB: db, DataDir: dataDir, PublicURL: normalizedPublicOrigin(u), assets: assets, Payments: payments.New(), limits: map[string]limit{}, keys: map[string]*keyLock{}}
	a.Chain = chain.New()
	a.cryptoWake = make(chan struct{}, 1)
	proxyCIDRs := os.Getenv("DONATE_TRUSTED_PROXIES")
	if proxyCIDRs == "" {
		proxyCIDRs = "127.0.0.1/32,::1/128"
	}
	if proxyCIDRs != "none" {
		for _, raw := range strings.Split(proxyCIDRs, ",") {
			_, block, err := net.ParseCIDR(strings.TrimSpace(raw))
			if err != nil {
				db.Close()
				return nil, errors.New("DONATE_TRUSTED_PROXIES must contain CIDRs, or none")
			}
			a.trustedProxies = append(a.trustedProxies, block)
		}
	}
	if e = a.migrate(); e != nil {
		db.Close()
		return nil, e
	}
	if a.Auth, e = auth.New(db, dataDir, a.PublicURL); e != nil {
		db.Close()
		return nil, e
	}
	if a.Donors, e = donors.New(db, a.PublicURL, donors.WithAdministratorPasskeys(a.Auth)); e != nil {
		db.Close()
		return nil, e
	}
	if a.Notify, e = notify.New(db); e != nil {
		db.Close()
		return nil, e
	}
	return a, nil
}
func (a *App) migrate() error {
	_, e := a.DB.Exec(`
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS donations(id TEXT PRIMARY KEY,status_token TEXT NOT NULL,amount_minor INTEGER NOT NULL CHECK(amount_minor>0),currency TEXT NOT NULL,method_id TEXT NOT NULL,method_type TEXT NOT NULL,method_name TEXT NOT NULL,name TEXT NOT NULL DEFAULT '',email TEXT NOT NULL DEFAULT '',message TEXT NOT NULL DEFAULT '',public INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL,source TEXT NOT NULL,provider_ref TEXT NOT NULL DEFAULT '',reference TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',checkout_url TEXT NOT NULL DEFAULT '',checkout_key TEXT NOT NULL DEFAULT '',checkout_digest TEXT NOT NULL DEFAULT '',donor_user_id TEXT NOT NULL DEFAULT '');
 CREATE UNIQUE INDEX IF NOT EXISTS donation_checkout_key ON donations(checkout_key) WHERE checkout_key<>'';
 CREATE INDEX IF NOT EXISTS donation_paid_at ON donations(status,paid_at);
 CREATE TABLE IF NOT EXISTS provider_events(provider TEXT NOT NULL,event_id TEXT NOT NULL,donation_id TEXT NOT NULL,PRIMARY KEY(provider,event_id));
 CREATE TABLE IF NOT EXISTS idempotency(key TEXT PRIMARY KEY,request_hash TEXT NOT NULL,status TEXT NOT NULL,response TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL);`)
	if e != nil {
		return e
	}
	if e = a.migrateDonorOwnership(); e != nil {
		return e
	}
	if e = a.migrateCheckoutExpiry(); e != nil {
		return e
	}
	if e = a.migrateProjects(); e != nil {
		return e
	}
	if e = a.migrateCrypto(); e != nil {
		return e
	}
	var count int
	if e = a.DB.QueryRow("SELECT count(*) FROM settings").Scan(&count); e != nil {
		return e
	}
	if count == 0 {
		return a.saveSettings(defaults())
	}
	return nil
}
func (a *App) Close() error { return a.DB.Close() }
func (a *App) Backup(path string) error {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	f.Close()
	_, e = a.DB.Exec("VACUUM INTO ?", absolute)
	if e != nil {
		// The empty exclusive destination stays private even if SQLite fails.
		return e
	}
	return os.Chmod(absolute, 0600)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	if e := d.Decode(v); e != nil {
		return errors.New("请求格式无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	a.Auth.Routes(mux)
	a.donorRoutes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := a.DB.PingContext(r.Context()); e != nil {
			fail(w, 503, "数据库暂不可用")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/site", a.site)
	a.cryptoRoutes(mux)
	mux.HandleFunc("GET /api/source", a.sourceInfo)
	mux.HandleFunc("GET /terms", a.legal)
	mux.HandleFunc("GET /privacy", a.legal)
	mux.HandleFunc("GET /api/stats", a.publicStats)
	mux.HandleFunc("GET /api/projects", a.listProjects)
	mux.HandleFunc("GET /api/projects/{id}", a.publicProjectHandler)
	mux.HandleFunc("GET /badge.svg", a.publicBadge)
	mux.HandleFunc("GET /api/private/stats", a.privateStats)
	mux.HandleFunc("POST /api/donations", a.createDonation)
	mux.HandleFunc("GET /api/donations/{id}", a.donationStatus)
	mux.HandleFunc("POST /api/donations/{id}/cancel", a.cancelDonation)
	mux.HandleFunc("POST /api/webhooks/{provider}", a.providerWebhook)
	mux.HandleFunc("GET /api/paypal/return", a.paypalReturn)
	mux.HandleFunc("GET /api/admin/settings", a.Auth.Require(a.getSettings))
	mux.HandleFunc("PUT /api/admin/settings", a.Auth.Require(a.putSettings))
	mux.HandleFunc("POST /api/admin/upload", a.Auth.Require(a.upload))
	mux.HandleFunc("GET /api/admin/projects", a.Auth.Require(a.listAdminProjects))
	mux.HandleFunc("POST /api/admin/projects", a.Auth.Require(a.createProject))
	mux.HandleFunc("PUT /api/admin/projects/{id}", a.Auth.Require(a.updateProject))
	mux.HandleFunc("GET /api/admin/donations", a.Auth.Require(a.listDonations))
	mux.HandleFunc("POST /api/admin/donations", a.Auth.Require(a.manualDonation))
	mux.HandleFunc("POST /api/admin/donations/{id}/confirm", a.Auth.Require(a.confirmDonation))
	mux.HandleFunc("GET /api/admin/export", a.Auth.Require(a.exportDonations))
	mux.HandleFunc("GET /api/admin/notifications", a.Auth.Require(a.listNotifications))
	mux.HandleFunc("POST /api/admin/notifications/{id}/retry", a.Auth.Require(a.retryNotification))
	mux.HandleFunc("GET /api/admin/waffo/stores", a.Auth.Require(a.waffoStores))
	mux.HandleFunc("GET /api/admin/waffo/products", a.Auth.Require(a.waffoProducts))
	mux.HandleFunc("POST /api/admin/waffo/products", a.Auth.Require(a.waffoMutate))
	mux.HandleFunc("PUT /api/admin/waffo/products/{id}", a.Auth.Require(a.waffoMutate))
	mux.HandleFunc("DELETE /api/admin/waffo/products/{id}", a.Auth.Require(a.waffoMutate))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	mux.HandleFunc("GET /uploads/{file}", a.serveUpload)
	mux.HandleFunc("/", a.serveAssets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := a.clientIP(r); ip != "" {
			r = r.Clone(r.Context())
			r.RemoteAddr = net.JoinHostPort(ip, "0")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), publickey-credentials-get=(self), publickey-credentials-create=(self)")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
		if strings.HasPrefix(a.PublicURL, "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) serveAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		fail(w, 405, "此资源只支持读取")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if path == "admin" || path == "admin/" {
		path = "admin.html"
	}
	if strings.Contains(path, "..") || strings.HasSuffix(path, ".go") {
		http.NotFound(w, r)
		return
	}
	if _, e := fs.Stat(a.assets, path); e != nil {
		http.NotFound(w, r)
		return
	}
	if path == "index.html" || path == "admin.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	content, err := fs.ReadFile(a.assets, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if path == "index.html" || path == "admin.html" {
		content, err = a.versionStaticHTML(content)
		if err != nil {
			http.Error(w, "页面资源加载失败", http.StatusInternalServerError)
			return
		}
	}
	sum := sha256.Sum256(content)
	version := hex.EncodeToString(sum[:])
	w.Header().Set("ETag", `"`+version+`"`)
	// Imported modules have stable URLs. Revalidate them so a new page cannot
	// accidentally reuse a previous release's payment logic. Content-versioned
	// dependencies keep their existing cache lifetime.
	if (strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css")) && r.URL.Query().Get("v") != version {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(content))
}

// HTML is revalidated on every load. Give its embedded dependencies URLs that
// change with their content, so a previous release's cached CSS or JS cannot be
// combined with the current HTML. Query strings do not alter the served path.
func (a *App) versionStaticHTML(content []byte) ([]byte, error) {
	var replacements []string
	for _, name := range []string{"favicon.svg", "favicon.ico", "apple-touch-icon.png", "site.webmanifest", "icons.svg", "style.css", "app.js", "admin.css", "admin.js", "coffee.css", "coffee.js"} {
		var references []string
		for _, attribute := range []string{"href", "src"} {
			for _, quote := range []string{`"`, `'`} {
				reference := attribute + "=" + quote + "/" + name + quote
				if bytes.Contains(content, []byte(reference)) {
					references = append(references, reference)
				}
			}
		}
		if len(references) == 0 {
			continue
		}
		resource, err := fs.ReadFile(a.assets, name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(resource)
		version := "?v=" + hex.EncodeToString(sum[:])
		for _, reference := range references {
			replacements = append(replacements, reference, reference[:len(reference)-1]+version+reference[len(reference)-1:])
		}
	}
	if len(replacements) == 0 {
		return content, nil
	}
	return []byte(strings.NewReplacer(replacements...).Replace(string(content))), nil
}
func (a *App) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && origin != a.PublicURL {
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func (a *App) rateLimit(r *http.Request) bool {
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	a.limitsMu.Lock()
	defer a.limitsMu.Unlock()
	now := time.Now()
	v := a.limits[ip]
	if now.After(v.Until) {
		v = limit{Until: now.Add(time.Minute)}
	}
	v.Count++
	a.limits[ip] = v
	if len(a.limits) > 10000 {
		for k, l := range a.limits {
			if now.After(l.Until) {
				delete(a.limits, k)
			}
		}
	}
	return v.Count <= 30
}
func (a *App) authorizedToken(r *http.Request, s Settings) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return len(token) > 0 && subtle.ConstantTimeCompare([]byte(token), []byte(s.StatsToken)) == 1
}
func (a *App) isTrusted(ip net.IP) bool {
	for _, block := range a.trustedProxies {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}
func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if !a.isTrusted(ip) {
		return ip.String()
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(parts) > 20 {
		return ip.String()
	}
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate == nil {
			return ip.String()
		}
		ip = candidate
		if !a.isTrusted(ip) {
			return ip.String()
		}
	}
	return ip.String()
}
func (a *App) lockCheckout(key string) func() {
	a.keysMu.Lock()
	lock := a.keys[key]
	if lock == nil {
		lock = &keyLock{}
		a.keys[key] = lock
	}
	lock.refs++
	a.keysMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		a.keysMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(a.keys, key)
		}
		a.keysMu.Unlock()
	}
}
func internalError(w http.ResponseWriter) { fail(w, 500, "处理失败，请稍后再试") }
