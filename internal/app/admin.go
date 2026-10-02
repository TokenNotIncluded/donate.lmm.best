package app

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) getSettings(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	respond(w, 200, s)
}
func (a *App) putSettings(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if e := decode(w, r, &s); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if e := validateSettings(s); e != nil {
		fail(w, 400, e.Error())
		return
	}
	a.checkoutMu.Lock()
	defer a.checkoutMu.Unlock()
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	old, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	// Existing orders retain their payment-provider identity and callback routing.
	for _, previous := range old.Methods {
		next, ok := methodByID(s, previous.ID)
		identityChanged := false
		fields := map[string][]string{"waffo": {"merchant_id", "private_key", "environment", "store_id"}, "paypal": {"client_id", "client_secret", "environment"}, "stripe": {"secret_key"}}[previous.Type]
		for _, key := range fields {
			if next.Config[key] != previous.Config[key] {
				identityChanged = true
			}
		}
		if !ok || next.Type != previous.Type || identityChanged {
			var count int
			if e = a.DB.QueryRow("SELECT count(*) FROM donations WHERE method_id=? AND source='checkout' AND method_type<>'custom'", previous.ID).Scan(&count); e != nil {
				internalError(w)
				return
			}
			if count > 0 {
				fail(w, 409, "此支付方式已有线上订单，不能更换商户、环境、凭据或店铺。请新建支付方式并停用原方式，保留原配置用于回调。")
				return
			}
		}
		if previous.Type == "waffo" {
			changed := false
			for _, key := range []string{"product_id", "tax_category", "language"} {
				if next.Config[key] != previous.Config[key] {
					changed = true
				}
			}
			if changed {
				var count int
				if e = a.DB.QueryRow("SELECT count(*) FROM donations WHERE method_id=? AND source='checkout' AND status='pending' AND checkout_url=''", previous.ID).Scan(&count); e != nil {
					internalError(w)
					return
				}
				if count > 0 {
					fail(w, 409, "此方式存在尚未完成创建的结账请求，不能更换产品配置。请先核对支付平台，或新建支付方式。")
					return
				}
			}
		}
	}
	if e = a.saveSettings(s); e != nil {
		internalError(w)
		return
	}
	respond(w, 200, map[string]bool{"saved": true})
}
func (a *App) listDonations(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "confirmed" && status != "refunded" {
		fail(w, 400, "状态无效")
		return
	}
	limit, offset := limitOffset(r)
	where := ""
	args := []any{}
	if status != "" {
		where = " WHERE status=?"
		args = append(args, status)
	}
	var count int
	if e := a.DB.QueryRow("SELECT count(*) FROM donations"+where, args...).Scan(&count); e != nil {
		internalError(w)
		return
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, e := a.DB.Query("SELECT "+donationColumns+" FROM donations"+where+" ORDER BY created_at DESC LIMIT ? OFFSET ?", queryArgs...)
	if e != nil {
		internalError(w)
		return
	}
	defer rows.Close()
	items := []Donation{}
	for rows.Next() {
		d, e := scanDonation(rows)
		if e != nil {
			internalError(w)
			return
		}
		items = append(items, d)
	}
	if rows.Err() != nil {
		internalError(w)
		return
	}
	respond(w, 200, map[string]any{"donations": items, "total": count})
}
func paidTime(raw string) (string, error) {
	if raw == "" {
		return time.Now().UTC().Format(time.RFC3339Nano), nil
	}
	t, e := time.Parse(time.RFC3339, raw)
	if e != nil {
		return "", e
	}
	if t.After(time.Now().Add(5*time.Minute)) || t.Year() < 2000 {
		return "", sql.ErrNoRows
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
func (a *App) manualDonation(w http.ResponseWriter, r *http.Request) {
	var in donationInput
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	if e = validateDonation(in, s); e != nil {
		fail(w, 400, e.Error())
		return
	}
	paid, e := paidTime(in.PaidAt)
	if e != nil {
		fail(w, 400, "到账时间需为有效 ISO 8601 时间，不能在未来")
		return
	}
	methodName := in.MethodName
	methodType := "custom"
	if m, ok := methodByID(s, in.MethodID); ok {
		methodName = m.Name
		methodType = m.Type
	}
	if in.MethodID == "" {
		in.MethodID = "manual"
	}
	if methodName == "" {
		methodName = "手动录入"
	}
	if !validID(in.MethodID) || len(methodName) > 120 {
		fail(w, 400, "支付方式无效")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && (!validID(key) || len(key) < 20) {
		fail(w, 400, "操作标识无效")
		return
	}
	if key != "" {
		key = "manual_" + key
	}
	raw, _ := json.Marshal(in)
	requestDigest := digest(string(raw))
	a.checkoutMu.Lock()
	defer a.checkoutMu.Unlock()
	if key != "" {
		d, err := scanDonation(a.DB.QueryRow("SELECT "+donationColumns+" FROM donations WHERE checkout_key=?", key))
		if err == nil {
			if d.CheckoutDigest != requestDigest {
				fail(w, 409, "操作标识已被其他记录使用")
				return
			}
			respond(w, 200, d)
			return
		}
		if err != sql.ErrNoRows {
			internalError(w)
			return
		}
	}
	d := Donation{ID: randomToken(), StatusToken: randomToken(), AmountMinor: in.AmountMinor, Currency: in.Currency, MethodID: in.MethodID, MethodType: methodType, MethodName: methodName, Name: in.Name, Email: in.Email, Message: in.Message, Public: in.Public, Status: "confirmed", Source: "manual", Reference: in.Reference, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), PaidAt: paid, CheckoutKey: key, CheckoutDigest: requestDigest}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	if e = insertDonation(tx, d); e != nil {
		internalError(w)
		return
	}
	if e = a.queueEvent(tx, d, "donation.confirmed", s); e != nil {
		internalError(w)
		return
	}
	if e = tx.Commit(); e != nil {
		internalError(w)
		return
	}
	respond(w, 201, d)
}
func (a *App) confirmDonation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reference string `json:"reference"`
		PaidAt    string `json:"paid_at"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if len(in.Reference) > 500 {
		fail(w, 400, "参考信息过长")
		return
	}
	paid, e := paidTime(in.PaidAt)
	if e != nil {
		fail(w, 400, "到账时间无效")
		return
	}
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	d, e := scanDonation(tx.QueryRow("SELECT "+donationColumns+" FROM donations WHERE id=?", r.PathValue("id")))
	if e != nil {
		fail(w, 404, "记录不存在")
		return
	}
	if d.MethodType != "custom" || d.Source != "checkout" {
		fail(w, 409, "线上支付需等待支付平台确认")
		return
	}
	if d.Status == "confirmed" {
		respond(w, 200, d)
		return
	}
	if d.Status != "pending" {
		fail(w, 409, "该状态不能确认")
		return
	}
	d.Status = "confirmed"
	d.PaidAt = paid
	d.Reference = in.Reference
	_, e = tx.Exec("UPDATE donations SET status='confirmed',paid_at=?,reference=? WHERE id=?", d.PaidAt, d.Reference, d.ID)
	if e != nil {
		internalError(w)
		return
	}
	if e = a.queueEvent(tx, d, "donation.confirmed", s); e != nil {
		internalError(w)
		return
	}
	if e = tx.Commit(); e != nil {
		internalError(w)
		return
	}
	respond(w, 200, d)
}
func safeCSV(v string) string {
	if strings.HasPrefix(v, "=") || strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") || strings.HasPrefix(v, "@") || strings.HasPrefix(v, "\t") || strings.HasPrefix(v, "\r") {
		return "'" + v
	}
	return v
}
func (a *App) exportDonations(w http.ResponseWriter, r *http.Request) {
	rows, e := a.DB.Query("SELECT " + donationColumns + " FROM donations ORDER BY created_at DESC")
	if e != nil {
		internalError(w)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="donations.csv"`)
	c := csv.NewWriter(w)
	defer c.Flush()
	_ = c.Write([]string{"id", "status", "amount_minor", "currency", "payment_method", "name", "email", "message", "public", "created_at", "paid_at", "source", "reference"})
	for rows.Next() {
		d, e := scanDonation(rows)
		if e != nil {
			return
		}
		_ = c.Write([]string{d.ID, d.Status, strconv.FormatInt(d.AmountMinor, 10), d.Currency, safeCSV(d.MethodName), safeCSV(d.Name), safeCSV(d.Email), safeCSV(d.Message), strconv.FormatBool(d.Public), d.CreatedAt, d.PaidAt, d.Source, safeCSV(d.Reference)})
	}
}
func (a *App) listNotifications(w http.ResponseWriter, r *http.Request) {
	jobs, e := a.Notify.List(r.Context())
	if e != nil {
		internalError(w)
		return
	}
	respond(w, 200, map[string]any{"notifications": jobs})
}
func (a *App) retryNotification(w http.ResponseWriter, r *http.Request) {
	if e := a.Notify.Retry(r.Context(), r.PathValue("id")); e != nil {
		fail(w, 400, "无法重试此通知，请检查状态")
		return
	}
	respond(w, 200, map[string]bool{"queued": true})
}
