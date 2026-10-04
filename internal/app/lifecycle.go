package app

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"time"
)

const hostedCheckoutLifetime = 45 * time.Minute
const lifecycleTimeFormat = "2006-01-02T15:04:05.000000000Z"

// Dates are written as UTC RFC3339Nano. Pad their fractional component before
// comparing, so old second-resolution dates and nanosecond dates sort alike.
const checkoutExpiryTime = "substr(expires_at,1,19) || '.' || substr(CASE WHEN substr(expires_at,20,1)='.' THEN substr(expires_at,21,length(expires_at)-21) ELSE '' END || '000000000',1,9) || 'Z'"
const expireCheckoutSQL = "UPDATE donations SET status='expired' WHERE status='pending' AND source='checkout' AND method_type NOT IN ('custom','crypto') AND expires_at<>'' AND (" + checkoutExpiryTime + ")<=?"

func (a *App) expireDonations(ctx context.Context, now time.Time) error {
	_, err := a.DB.ExecContext(ctx, expireCheckoutSQL, now.UTC().Format(lifecycleTimeFormat))
	return err
}

func (a *App) expireDonation(ctx context.Context, id string, now time.Time) error {
	_, err := a.DB.ExecContext(ctx, expireCheckoutSQL+" AND id=?", now.UTC().Format(lifecycleTimeFormat), id)
	return err
}

// Run joins both background workers before database shutdown. A persisted
// deadline is also checked on startup and by receipt reads after a restart.
func (a *App) Run(ctx context.Context) {
	cryptoDone := make(chan struct{})
	go func() { defer close(cryptoDone); a.runCrypto(ctx) }()
	notificationsDone := make(chan struct{})
	go func() {
		defer close(notificationsDone)
		a.Notify.Run(ctx)
	}()
	a.runCheckoutExpiry(ctx)
	<-notificationsDone
	<-cryptoDone
}

func (a *App) runCheckoutExpiry(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.expireDonations(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Print("donation expiration temporarily failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func receiptStatus(d Donation) map[string]any {
	return map[string]any{"id": d.ID, "status": d.Status, "amount_minor": d.AmountMinor, "currency": d.Currency, "method_name": d.MethodName, "paid_at": d.PaidAt, "custom": d.MethodType == "custom", "expires_at": d.ExpiresAt, "can_cancel": d.Status == "pending" && d.Source == "checkout", "project_id": d.ProjectID, "project_name": d.ProjectName, "public_thanks": d.PublicThanks}
}

func (a *App) cancelDonation(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) || !a.Donors.CheckOrigin(r) {
		fail(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if !a.rateLimit(r) {
		fail(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}
	userID, err := a.checkoutDonor(r)
	if err != nil {
		fail(w, http.StatusForbidden, "捐赠账号会话或请求验证无效，请重新登录")
		return
	}
	var in struct {
		StatusToken string `json:"status_token"`
	}
	if err = decode(w, r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// A PayPal capture already being sent cannot be undone locally. Serialize
	// its initiation with cancellation rather than initiating a new capture
	// after a successful local cancellation.
	unlock := a.lockCheckout("capture_" + r.PathValue("id"))
	defer unlock()
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	d, err := scanDonation(tx.QueryRow("SELECT "+donationColumns+" FROM donations WHERE id=?", r.PathValue("id")))
	if err != nil || d.StatusToken == "" || subtle.ConstantTimeCompare([]byte(in.StatusToken), []byte(d.StatusToken)) != 1 {
		fail(w, http.StatusNotFound, "记录不存在或访问令牌无效")
		return
	}
	if d.MethodType == "crypto" {
		fail(w, 409, "链上转账不能取消或自动退款")
		return
	}
	if userID != "" && d.DonorUserID != "" && d.DonorUserID != userID {
		fail(w, http.StatusForbidden, "不能取消其他账号的捐赠")
		return
	}
	if _, err = tx.Exec(expireCheckoutSQL+" AND id=?", time.Now().UTC().Format(lifecycleTimeFormat), d.ID); err != nil {
		internalError(w)
		return
	}
	if d, err = scanDonation(tx.QueryRow("SELECT "+donationColumns+" FROM donations WHERE id=?", d.ID)); err != nil {
		internalError(w)
		return
	}
	if d.Source != "checkout" || !contains([]string{"pending", "cancelled", "expired"}, d.Status) {
		fail(w, http.StatusConflict, "付款已经确认，不能取消或自动退款")
		return
	}
	if d.Status == "pending" {
		if _, err = tx.Exec("UPDATE donations SET status='cancelled' WHERE id=? AND status='pending'", d.ID); err != nil {
			internalError(w)
			return
		}
		d.Status = "cancelled"
	}
	if err = tx.Commit(); err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, receiptStatus(d))
}
