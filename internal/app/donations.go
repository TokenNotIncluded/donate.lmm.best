package app

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

type Donation struct {
	ID             string `json:"id"`
	StatusToken    string `json:"-"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	MethodID       string `json:"method_id"`
	MethodType     string `json:"method_type"`
	MethodName     string `json:"method_name"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	Message        string `json:"message"`
	Public         bool   `json:"public"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	ProviderRef    string `json:"-"`
	Reference      string `json:"reference"`
	CreatedAt      string `json:"created_at"`
	PaidAt         string `json:"paid_at"`
	CheckoutURL    string `json:"-"`
	CheckoutKey    string `json:"-"`
	CheckoutDigest string `json:"-"`
	DonorUserID    string `json:"donor_user_id,omitempty"`
}

const donationColumns = "id,status_token,amount_minor,currency,method_id,method_type,method_name,name,email,message,public,status,source,provider_ref,reference,created_at,paid_at,checkout_url,checkout_key,checkout_digest,donor_user_id"

type scanner interface{ Scan(...any) error }

func scanDonation(s scanner) (Donation, error) {
	var d Donation
	e := s.Scan(&d.ID, &d.StatusToken, &d.AmountMinor, &d.Currency, &d.MethodID, &d.MethodType, &d.MethodName, &d.Name, &d.Email, &d.Message, &d.Public, &d.Status, &d.Source, &d.ProviderRef, &d.Reference, &d.CreatedAt, &d.PaidAt, &d.CheckoutURL, &d.CheckoutKey, &d.CheckoutDigest, &d.DonorUserID)
	return d, e
}
func (a *App) donation(id string) (Donation, error) {
	return scanDonation(a.DB.QueryRow("SELECT "+donationColumns+" FROM donations WHERE id=?", id))
}
func insertDonation(tx *sql.Tx, d Donation) error {
	_, e := tx.Exec("INSERT INTO donations("+donationColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", d.ID, d.StatusToken, d.AmountMinor, d.Currency, d.MethodID, d.MethodType, d.MethodName, d.Name, d.Email, d.Message, d.Public, d.Status, d.Source, d.ProviderRef, d.Reference, d.CreatedAt, d.PaidAt, d.CheckoutURL, d.CheckoutKey, d.CheckoutDigest, d.DonorUserID)
	return e
}
func methodByID(s Settings, id string) (payments.Method, bool) {
	for _, m := range s.Methods {
		if m.ID == id {
			return m, true
		}
	}
	return payments.Method{}, false
}

type donationInput struct {
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	MethodID      string `json:"method_id"`
	MethodName    string `json:"method_name"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Message       string `json:"message"`
	Public        bool   `json:"public"`
	AcceptedTerms bool   `json:"accepted_terms"`
	PaidAt        string `json:"paid_at"`
	Reference     string `json:"reference"`
}

func validateDonation(in donationInput, s Settings) error {
	factor, ok := supportedCurrencies[in.Currency]
	if !ok || !contains(s.Site.Currencies, in.Currency) {
		return errors.New("此币种暂不支持")
	}
	if in.AmountMinor < 1 || in.AmountMinor > 1000000*factor {
		return errors.New("金额应大于零且不能超过 1000000")
	}
	if len(in.Name) > 120 || len(in.Email) > 254 || len(in.Message) > 2000 || len(in.Reference) > 500 {
		return errors.New("名字、邮箱或留言过长")
	}
	if in.Email != "" {
		addr, e := mail.ParseAddress(in.Email)
		if e != nil || addr.Address != in.Email {
			return errors.New("请填写有效邮箱")
		}
	}
	return nil
}
func (a *App) createDonation(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) {
		fail(w, 403, "请求来源无效")
		return
	}
	if !a.rateLimit(r) {
		fail(w, 429, "请求过于频繁，请稍后再试")
		return
	}
	donorUserID, e := a.checkoutDonor(r)
	if e != nil {
		fail(w, http.StatusForbidden, "捐赠账号会话或请求验证无效，请重新登录")
		return
	}
	var in donationInput
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if !in.AcceptedTerms {
		fail(w, 400, "请先阅读并同意用户协议与隐私政策")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && (!validID(key) || len(key) < 20) {
		fail(w, 400, "Idempotency-Key 必须为 20 到 80 位唯一标识")
		return
	}
	lockKey := key
	if lockKey == "" {
		lockKey = randomToken()
	}
	unlock := a.lockCheckout(lockKey)
	defer unlock()
	a.checkoutMu.Lock()
	settingsLocked := true
	defer func() {
		if settingsLocked {
			a.checkoutMu.Unlock()
		}
	}()
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	if !s.Site.CollectName {
		in.Name = ""
	}
	if !s.Site.CollectEmail {
		in.Email = ""
	}
	if !s.Site.CollectMessage {
		in.Message = ""
	}
	if e = validateDonation(in, s); e != nil {
		fail(w, 400, e.Error())
		return
	}
	m, ok := methodByID(s, in.MethodID)
	if !ok || !m.Enabled {
		fail(w, 400, "请选择可用的支付方式")
		return
	}
	raw, _ := json.Marshal(in)
	requestDigest := digest(string(raw))
	if donorUserID != "" {
		requestDigest = digest(string(raw) + "\n" + "donor:" + donorUserID)
	}
	var d Donation
	if key != "" {
		d, e = scanDonation(a.DB.QueryRow("SELECT "+donationColumns+" FROM donations WHERE checkout_key=?", key))
		if e != nil && e != sql.ErrNoRows {
			internalError(w)
			return
		}
		if e == nil {
			if d.DonorUserID != donorUserID || d.CheckoutDigest != requestDigest {
				fail(w, 409, "同一个操作标识不能用于不同的捐赠")
				return
			}
			if d.Status != "pending" || d.CheckoutURL != "" || m.Type == "custom" {
				a.checkoutResponse(w, d, m)
				return
			}
		}
	}
	if d.ID == "" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		d = Donation{ID: randomToken(), StatusToken: randomToken(), AmountMinor: in.AmountMinor, Currency: in.Currency, MethodID: m.ID, MethodType: m.Type, MethodName: m.Name, Name: strings.TrimSpace(in.Name), Email: in.Email, Message: strings.TrimSpace(in.Message), Public: in.Public, Status: "pending", Source: "checkout", CreatedAt: now, CheckoutKey: key, CheckoutDigest: requestDigest, DonorUserID: donorUserID}
		if err := payments.ValidateCheckout(m, a.checkoutRequest(d, m)); err != nil {
			fail(w, 400, payments.SafeError(err))
			return
		}
		tx, err := a.DB.BeginTx(r.Context(), nil)
		if err != nil {
			internalError(w)
			return
		}
		if err = insertDonation(tx, d); err != nil {
			tx.Rollback()
			internalError(w)
			return
		}
		if err = tx.Commit(); err != nil {
			internalError(w)
			return
		}
	}
	if m.Type == "custom" {
		a.checkoutResponse(w, d, m)
		return
	}
	a.checkoutMu.Unlock()
	settingsLocked = false
	created, parseErr := time.Parse(time.RFC3339Nano, d.CreatedAt)
	// PayPal's shortest documented request-key retention is six hours. An
	// uncertain checkout must never silently become a second charge later.
	if parseErr != nil || time.Since(created) >= 5*time.Hour {
		fail(w, 409, "此结账请求已超出安全重试期限。请先核对支付平台，再发起新的捐赠。")
		return
	}
	result, e := a.Payments.Checkout(r.Context(), m, a.checkoutRequest(d, m))
	if e != nil {
		respond(w, 502, map[string]any{"error": payments.SafeError(e), "id": d.ID, "status_token": d.StatusToken})
		return
	}
	if _, e = a.DB.Exec("UPDATE donations SET provider_ref=?,checkout_url=? WHERE id=? AND status='pending'", result.Reference, result.URL, d.ID); e != nil {
		internalError(w)
		return
	}
	d.ProviderRef = result.Reference
	d.CheckoutURL = result.URL
	a.checkoutResponse(w, d, m)
}
func (a *App) checkoutRequest(d Donation, m payments.Method) payments.CheckoutRequest {
	returnURL := a.PublicURL + "/?donation=" + url.QueryEscape(d.ID) + "&status_token=" + url.QueryEscape(d.StatusToken)
	if m.Type == "paypal" {
		returnURL = a.PublicURL + "/api/paypal/return?donation=" + url.QueryEscape(d.ID) + "&status_token=" + url.QueryEscape(d.StatusToken)
	}
	return payments.CheckoutRequest{ID: d.ID, AmountMinor: d.AmountMinor, Currency: d.Currency, Name: d.Name, Email: d.Email, ReturnURL: returnURL, CancelURL: a.PublicURL + "/?cancelled=1", WebhookURL: a.PublicURL + "/api/webhooks/" + m.Type + "?method_id=" + url.QueryEscape(m.ID)}
}
func (a *App) checkoutResponse(w http.ResponseWriter, d Donation, m payments.Method) {
	checkoutURL := d.CheckoutURL
	if m.Type == "custom" {
		checkoutURL = m.CheckoutURL
	}
	respond(w, 200, map[string]any{"id": d.ID, "status": d.Status, "checkout_url": checkoutURL, "qr_url": m.QRURL, "instructions": m.Description, "status_token": d.StatusToken})
}
func (a *App) donationStatus(w http.ResponseWriter, r *http.Request) {
	d, e := a.donation(r.PathValue("id"))
	if e != nil || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(d.StatusToken)) != 1 {
		fail(w, 404, "记录不存在或访问令牌无效")
		return
	}
	response := map[string]any{"id": d.ID, "status": d.Status, "amount_minor": d.AmountMinor, "currency": d.Currency, "method_name": d.MethodName, "paid_at": d.PaidAt, "custom": d.MethodType == "custom"}
	if d.Status == "pending" {
		response["checkout_url"] = d.CheckoutURL
		if s, err := a.settings(); err == nil {
			if m, ok := methodByID(s, d.MethodID); ok && m.Type == "custom" {
				response["checkout_url"] = m.CheckoutURL
				response["qr_url"] = m.QRURL
				response["instructions"] = m.Description
			}
		}
	}
	respond(w, 200, response)
}
func (a *App) queueEvent(tx *sql.Tx, d Donation, eventType string, s Settings) error {
	// This is the sole event payload; configuration/credentials never enter notifications.
	event := notify.Event{ID: eventType + "_" + d.ID, Type: eventType, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Donation: map[string]any{"id": d.ID, "amount_minor": d.AmountMinor, "currency": d.Currency, "method_id": d.MethodID, "method_name": d.MethodName, "name": d.Name, "email": d.Email, "message": d.Message, "paid_at": d.PaidAt, "source": d.Source}}
	if d.DonorUserID != "" {
		event.Donation.(map[string]any)["donor_user_id"] = d.DonorUserID
	}
	return a.Notify.Queue(tx, event, notify.Config{Webhook: s.Webhook, SMTP: s.SMTP})
}
func (a *App) settle(r *http.Request, provider string, c payments.Confirmation, s Settings) error {
	if c.EventID == "" || c.Reference == "" || (!c.Paid && !c.Refunded) {
		return errors.New("无可确认的支付事件")
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var already int
	e = tx.QueryRow("SELECT count(*) FROM provider_events WHERE provider=? AND event_id=?", provider, c.EventID).Scan(&already)
	if e != nil {
		return e
	}
	if already > 0 {
		return nil
	}
	var d Donation
	if c.DonationID != "" {
		d, e = scanDonation(tx.QueryRow("SELECT "+donationColumns+" FROM donations WHERE id=?", c.DonationID))
	} else {
		d, e = scanDonation(tx.QueryRow("SELECT "+donationColumns+" FROM donations WHERE method_type=? AND provider_ref=?", provider, c.Reference))
	}
	if e != nil {
		return e
	}
	if d.MethodType != provider || d.Source != "checkout" || d.AmountMinor != c.AmountMinor || d.Currency != strings.ToUpper(c.Currency) {
		return errors.New("支付金额、币种或方式与订单不符")
	}
	if d.ProviderRef != "" && d.ProviderRef != c.Reference && !(provider == "waffo" && d.Status == "pending" && c.Paid && !c.Refunded) {
		return errors.New("支付订单标识不符")
	}
	target := "confirmed"
	eventType := "donation.confirmed"
	if c.Refunded {
		target = "refunded"
		eventType = "donation.refunded"
		if d.Status != "confirmed" && d.Status != "refunded" {
			return errors.New("不能退款未确认订单")
		}
	}
	if !c.Refunded && d.Status == "refunded" {
		return errors.New("退款记录不能再次确认")
	}
	if d.Status != target {
		if d.PaidAt == "" {
			d.PaidAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		_, e = tx.Exec("UPDATE donations SET status=?,paid_at=?,provider_ref=? WHERE id=?", target, d.PaidAt, c.Reference, d.ID)
		if e != nil {
			return e
		}
		d.Status = target
		d.ProviderRef = c.Reference
		if e = a.queueEvent(tx, d, eventType, s); e != nil {
			return e
		}
	}
	_, e = tx.Exec("INSERT INTO provider_events(provider,event_id,donation_id) VALUES(?,?,?)", provider, c.EventID, d.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) providerWebhook(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	provider := r.PathValue("provider")
	m, ok := a.callbackMethod(s, provider, r.URL.Query().Get("method_id"))
	if !ok {
		fail(w, 400, "支付方式配置不明确")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	body, e := readBody(r)
	if e != nil {
		fail(w, 400, "回调内容过大或无效")
		return
	}
	c, e := a.Payments.VerifyWebhook(r.Context(), m, r.Header, body)
	if e != nil {
		fail(w, 400, "回调签名或内容验证失败")
		return
	}
	if !c.Paid && !c.Refunded {
		respond(w, 200, map[string]bool{"received": true})
		return
	}
	// Require the matched method, not merely the provider type, to own this order.
	if c.DonationID != "" {
		d, err := a.donation(c.DonationID)
		if err != nil || d.MethodID != m.ID {
			fail(w, 400, "回调订单不匹配")
			return
		}
	} else {
		var method string
		if a.DB.QueryRow("SELECT method_id FROM donations WHERE method_type=? AND provider_ref=?", provider, c.Reference).Scan(&method) != nil || method != m.ID {
			fail(w, 400, "回调订单不匹配")
			return
		}
	}
	if e = a.settle(r, provider, c, s); e != nil {
		fail(w, 400, "回调与订单记录不匹配")
		return
	}
	respond(w, 200, map[string]bool{"received": true})
}
func (a *App) callbackMethod(s Settings, provider, id string) (payments.Method, bool) {
	if id != "" {
		m, ok := methodByID(s, id)
		return m, ok && m.Type == provider && provider != "custom"
	}
	var found payments.Method
	count := 0
	for _, m := range s.Methods {
		if m.Type == provider && provider != "custom" {
			found = m
			count++
		}
	}
	return found, count == 1
}
func (a *App) paypalReturn(w http.ResponseWriter, r *http.Request) {
	d, e := a.donation(r.URL.Query().Get("donation"))
	if e != nil || d.MethodType != "paypal" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("status_token")), []byte(d.StatusToken)) != 1 || r.URL.Query().Get("token") != d.ProviderRef {
		fail(w, 400, "付款返回信息无效")
		return
	}
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	m, ok := methodByID(s, d.MethodID)
	if !ok || m.Type != "paypal" {
		fail(w, 400, "支付方式配置已更改")
		return
	}
	if d.Status == "pending" {
		c, err := a.Payments.CapturePayPal(r.Context(), m, d.ProviderRef)
		if err != nil || !c.Paid {
			fail(w, 502, "PayPal 尚未完成付款。请稍后检查捐赠状态。")
			return
		}
		if c.DonationID == "" {
			c.DonationID = d.ID
		}
		if e = a.settle(r, "paypal", c, s); e != nil {
			fail(w, 400, "PayPal 付款与订单不匹配")
			return
		}
	}
	http.Redirect(w, r, a.PublicURL+"/?donation="+url.QueryEscape(d.ID)+"&status_token="+url.QueryEscape(d.StatusToken), http.StatusSeeOther)
}
func limitOffset(r *http.Request) (int, int) {
	l, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if l <= 0 || l > 500 {
		l = 100
	}
	o, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if o < 0 {
		o = 0
	}
	return l, o
}
func readBody(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }
