package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/chain"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/skip2/go-qrcode"
)

func (a *App) migrateCrypto() error {
	_, e := a.DB.Exec(`CREATE TABLE IF NOT EXISTS crypto_payments(
 donation_id TEXT PRIMARY KEY REFERENCES donations(id), quote TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'open', received_units TEXT NOT NULL DEFAULT '0',
 reason TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 0, deadline INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS crypto_transactions(
 donation_id TEXT NOT NULL REFERENCES crypto_payments(donation_id), network TEXT NOT NULL, tx_id TEXT NOT NULL,
 evidence TEXT NOT NULL, state TEXT NOT NULL, submitted_at INTEGER NOT NULL, next_check INTEGER NOT NULL,
 retries INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(donation_id,network,tx_id));
 CREATE INDEX IF NOT EXISTS crypto_due ON crypto_transactions(next_check,state);
 CREATE TABLE IF NOT EXISTS crypto_claims(network TEXT NOT NULL,tx_id TEXT NOT NULL,donation_id TEXT NOT NULL REFERENCES donations(id),PRIMARY KEY(network,tx_id));
 CREATE TABLE IF NOT EXISTS crypto_hook_events(event_id TEXT PRIMARY KEY,created_at INTEGER NOT NULL);`)
	return e
}
func (a *App) createCrypto(tx *sql.Tx, d Donation, q chain.Quote) error {
	raw, e := json.Marshal(q)
	if e != nil {
		return e
	}
	expiry, e := time.Parse(time.RFC3339Nano, d.ExpiresAt)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO crypto_payments(donation_id,quote,deadline) VALUES(?,?,?)", d.ID, string(raw), expiry.Add(time.Duration(q.ConfirmationHours)*time.Hour).Unix())
	return e
}

type cryptoPayment struct {
	Quote                   chain.Quote
	State, Received, Reason string
	Revision, Deadline      int64
}

func readCrypto(db interface{ QueryRow(string, ...any) *sql.Row }, id string) (cryptoPayment, error) {
	var p cryptoPayment
	var raw string
	e := db.QueryRow("SELECT quote,state,received_units,reason,revision,deadline FROM crypto_payments WHERE donation_id=?", id).Scan(&raw, &p.State, &p.Received, &p.Reason, &p.Revision, &p.Deadline)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &p.Quote)
	}
	return p, e
}

type cryptoTransaction struct {
	ID       string         `json:"tx_id"`
	Evidence chain.Evidence `json:"result"`
}

func (a *App) cryptoView(id string) (map[string]any, error) {
	p, e := readCrypto(a.DB, id)
	if e != nil {
		return nil, e
	}
	q := p.Quote
	rows, e := a.DB.Query("SELECT tx_id,evidence FROM crypto_transactions WHERE donation_id=? ORDER BY submitted_at,tx_id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	txs := []cryptoTransaction{}
	observed := new(big.Int)
	for rows.Next() {
		var t cryptoTransaction
		var raw string
		if e = rows.Scan(&t.ID, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &t.Evidence); e != nil {
			return nil, e
		}
		txs = append(txs, t)
		if t.Evidence.State == "valid" || t.Evidence.State == "confirming" {
			v, _ := chain.Units(t.Evidence.Amount)
			if v != nil {
				observed.Add(observed, v)
			}
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	received, _ := chain.Units(p.Received)
	expected, _ := chain.Units(q.Expected)
	remaining := new(big.Int).Sub(expected, received)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	excess := new(big.Int).Sub(received, expected)
	if excess.Sign() < 0 {
		excess.SetInt64(0)
	}
	uri := chain.PaymentURI(q, remaining.String())
	return map[string]any{"state": p.State, "reason": p.Reason, "network": q.Network.ID, "network_name": q.Network.Name, "family": q.Network.Family, "chain_id": q.Network.ChainID, "wallet_rpc_url": q.Network.WalletRPCURL, "native_symbol": q.Network.NativeSymbol, "asset": q.Token.Asset, "token_label": q.Token.Label, "token_contract": q.Token.Contract, "decimals": q.Token.Decimals, "recipient": q.Recipient, "expected_units": q.Expected, "expected": chain.Decimal(q.Expected, q.Token.Decimals), "received_units": p.Received, "received": chain.Decimal(p.Received, q.Token.Decimals), "observed": chain.Decimal(observed.String(), q.Token.Decimals), "remaining_units": remaining.String(), "remaining": chain.Decimal(remaining.String(), q.Token.Decimals), "overpaid": chain.Decimal(excess.String(), q.Token.Decimals), "minimum": chain.Decimal(chain.Minimum(q.Expected, q.ToleranceBPS).String(), q.Token.Decimals), "tolerance_bps": q.ToleranceBPS, "payment_uri": uri, "explorer": q.Network.Explorer, "transactions": txs, "accept_late": q.AcceptLate, "revision": p.Revision}, nil
}
func (a *App) cryptoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/donations/{id}/transactions", a.submitCrypto)
	mux.HandleFunc("POST /api/donations/{id}/recheck", a.recheckCrypto)
	mux.HandleFunc("GET /api/donations/{id}/events", a.receiptEvents)
	mux.HandleFunc("GET /api/donations/{id}/qr", a.cryptoQR)
	mux.HandleFunc("POST /api/donations/{id}/wallet", a.cryptoWalletData)
	mux.HandleFunc("POST /api/crypto/events", a.chainHook)
	mux.HandleFunc("POST /api/admin/crypto/check", a.Auth.Require(a.checkCryptoNetwork))
	mux.HandleFunc("GET /api/admin/crypto/transactions/{id}", a.Auth.Require(a.adminCrypto))
}
func (a *App) authorizeCrypto(r *http.Request, token string) (Donation, error) {
	d, e := a.donation(r.PathValue("id"))
	if e != nil || d.MethodType != "crypto" || d.StatusToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(d.StatusToken)) != 1 {
		return Donation{}, errors.New("receipt_unavailable")
	}
	return d, nil
}
func (a *App) submitCrypto(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) {
		fail(w, 403, "请求来源无效")
		return
	}
	if !a.rateLimit(r) {
		fail(w, 429, "请求过于频繁")
		return
	}
	var in struct {
		Token   string `json:"status_token"`
		Network string `json:"network"`
		TxID    string `json:"tx_id"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	d, e := a.authorizeCrypto(r, in.Token)
	if e != nil {
		fail(w, 404, "记录不存在或访问令牌无效")
		return
	}
	unlock := a.lockCheckout("crypto_" + d.ID)
	defer unlock()
	p, e := readCrypto(a.DB, d.ID)
	if e != nil {
		internalError(w)
		return
	}
	if in.Network != p.Quote.Network.ID {
		fail(w, 400, "请选择此订单的付款网络")
		return
	}
	id, e := chain.NormalizeTx(p.Quote.Network.Family, in.TxID)
	if e != nil {
		respond(w, 400, map[string]any{"error": "交易 ID 格式无效", "reason": "invalid_tx_id"})
		return
	}
	if d.Status == "confirmed" || d.Status == "refunded" {
		a.cryptoReceiptResponse(w, d.ID)
		return
	}
	if time.Now().Unix() > p.Deadline {
		respond(w, 409, map[string]any{"error": "已超出链上确认期限，请联系维护者核对转账", "reason": "confirmation_timeout"})
		return
	}
	var count int
	if e = a.DB.QueryRow("SELECT count(*) FROM crypto_transactions WHERE donation_id=?", d.ID).Scan(&count); e != nil {
		internalError(w)
		return
	}
	if count >= 20 {
		var exists int
		a.DB.QueryRow("SELECT count(*) FROM crypto_transactions WHERE donation_id=? AND tx_id=?", d.ID, id).Scan(&exists)
		if exists == 0 {
			fail(w, 429, "此订单交易提交次数已达上限，请联系维护者")
			return
		}
	}
	raw, _ := json.Marshal(chain.Evidence{State: "submitted", Reason: "not_found", Amount: "0"})
	_, e = a.DB.Exec("INSERT INTO crypto_transactions(donation_id,network,tx_id,evidence,state,submitted_at,next_check) VALUES(?,?,?,?,?,?,?) ON CONFLICT(donation_id,network,tx_id) DO NOTHING", d.ID, in.Network, id, string(raw), "submitted", time.Now().Unix(), time.Now().Unix())
	if e != nil {
		internalError(w)
		return
	}
	if e = a.verifyCrypto(r.Context(), d.ID, id); e != nil {
		internalError(w)
		return
	}
	a.wakeCrypto()
	a.cryptoReceiptResponse(w, d.ID)
}
func (a *App) cryptoReceiptResponse(w http.ResponseWriter, id string) {
	d, e := a.donation(id)
	if e != nil {
		internalError(w)
		return
	}
	v, e := a.cryptoView(id)
	if e != nil {
		internalError(w)
		return
	}
	r := receiptStatus(d)
	r["crypto"] = v
	r["can_cancel"] = false
	respond(w, 200, r)
}

// Called with the donation's lock held. RPC happens before opening the database
// transaction; claims, settlement and notification enqueue commit together.
func (a *App) verifyCrypto(ctx context.Context, id, txID string) error {
	p, e := readCrypto(a.DB, id)
	if e != nil {
		return e
	}
	d, e := a.donation(id)
	if e != nil {
		return e
	}
	if d.Status == "confirmed" || d.Status == "refunded" {
		return nil
	}
	var submitted int64
	var previousRaw string
	var retries int
	if e = a.DB.QueryRow("SELECT submitted_at,evidence,retries FROM crypto_transactions WHERE donation_id=? AND tx_id=?", id, txID).Scan(&submitted, &previousRaw, &retries); e != nil {
		return e
	}
	var previous chain.Evidence
	if e = json.Unmarshal([]byte(previousRaw), &previous); e != nil {
		return e
	}
	if previous.State == "valid" && previous.Final {
		return nil
	}
	v, verifyErr := a.Chain.Verify(ctx, p.Quote, txID)
	if verifyErr != nil {
		v = previous
		v.Reason = verifyErr.Error()
		if v.State == "valid" || v.State == "invalid" || v.State == "failed" {
			v.State = "submitted"
			v.Final = false
		}
	}
	now := time.Now().Unix()
	if now > p.Deadline {
		v.State = "failed"
		v.Reason = "confirmation_timeout"
		v.Final = false
	}
	if v.State == "valid" || v.State == "confirming" {
		created, _ := time.Parse(time.RFC3339Nano, d.CreatedAt)
		expiry, _ := time.Parse(time.RFC3339Nano, d.ExpiresAt)
		if v.Time == 0 {
			v.State = "submitted"
			v.Reason = "block_time_unavailable"
			v.Final = false
		} else if v.Time < created.Unix()-30 {
			v.State = "invalid"
			v.Reason = "transaction_too_old"
			v.Final = false
		} else if !p.Quote.AcceptLate && v.Time > expiry.Unix() && submitted > expiry.Unix() {
			v.State = "invalid"
			v.Reason = "late_payment"
			v.Final = false
		}
	}
	s, e := a.settings()
	if e != nil {
		return e
	}
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// The stricter whole-transaction claim protects batched transfer logs too.
	if v.State == "valid" || v.State == "confirming" {
		if _, e = tx.Exec("INSERT INTO crypto_claims(network,tx_id,donation_id) VALUES(?,?,?) ON CONFLICT(network,tx_id) DO NOTHING", p.Quote.Network.ReplayKey(), txID, id); e != nil {
			return e
		}
		var owner string
		if e = tx.QueryRow("SELECT donation_id FROM crypto_claims WHERE network=? AND tx_id=?", p.Quote.Network.ReplayKey(), txID).Scan(&owner); e != nil {
			return e
		}
		if owner != id {
			v.State = "invalid"
			v.Reason = "already_used"
			v.Final = false
		}
	}
	next := int64(0)
	if v.State == "submitted" || v.State == "confirming" {
		next = now + confirmationDelay(retries)
		if next > p.Deadline {
			next = p.Deadline
		}
	}
	raw, _ := json.Marshal(v)
	if _, e = tx.Exec("UPDATE crypto_transactions SET evidence=?,state=?,next_check=?,retries=retries+1 WHERE donation_id=? AND tx_id=?", string(raw), v.State, next, id, txID); e != nil {
		return e
	}
	rows, e := tx.Query("SELECT evidence FROM crypto_transactions WHERE donation_id=?", id)
	if e != nil {
		return e
	}
	sum := new(big.Int)
	hasWaiting := false
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		var item chain.Evidence
		if e = json.Unmarshal([]byte(raw), &item); e != nil {
			rows.Close()
			return e
		}
		if item.State == "valid" && item.Final {
			n, e := chain.Units(item.Amount)
			if e != nil {
				rows.Close()
				return e
			}
			sum.Add(sum, n)
		}
		if item.State == "submitted" || item.State == "confirming" {
			hasWaiting = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	state, reason := v.State, v.Reason
	expected, _ := chain.Units(p.Quote.Expected)
	if sum.Sign() > 0 && sum.Cmp(chain.Minimum(p.Quote.Expected, p.Quote.ToleranceBPS)) >= 0 {
		state = "paid"
		if sum.Cmp(expected) > 0 {
			state = "overpaid"
		}
		reason = ""
	} else if hasWaiting {
		state = "confirming"
		if v.State == "submitted" {
			state = "submitted"
		}
	} else if sum.Sign() > 0 {
		state = "partial"
		reason = "underpaid"
	}
	if now > p.Deadline && state != "paid" && state != "overpaid" {
		state = "failed"
		reason = "confirmation_timeout"
	}
	changed := p.State != state || p.Received != sum.String() || p.Reason != reason || previousRaw != string(raw)
	if changed {
		p.Revision++
		if _, e = tx.Exec("UPDATE crypto_payments SET state=?,received_units=?,reason=?,revision=? WHERE donation_id=?", state, sum.String(), reason, p.Revision, id); e != nil {
			return e
		}
		if state == "paid" || state == "overpaid" {
			// Fiat statistics record the actual receipt rounded to cents, with the full
			// precision amount and original expectation retained in crypto_payments.
			factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p.Quote.Token.Decimals-2)), nil)
			actual := new(big.Int).Add(sum, new(big.Int).Div(new(big.Int).Set(factor), big.NewInt(2)))
			actual.Div(actual, factor)
			if !actual.IsInt64() || actual.Sign() <= 0 || actual.Int64() > 1_000_000_000_000 {
				return errors.New("received amount outside ledger range")
			}
			d.Status = "confirmed"
			d.PaidAt = time.Now().UTC().Format(time.RFC3339Nano)
			d.AmountMinor = actual.Int64()
			if _, e = tx.Exec("UPDATE donations SET status='confirmed',paid_at=?,amount_minor=?,reference=? WHERE id=? AND status<>'confirmed'", d.PaidAt, d.AmountMinor, txID, id); e != nil {
				return e
			}
			if e = a.queueEvent(tx, d, "donation.confirmed", s); e != nil {
				return e
			}
		}
		if e = a.queueCryptoEvent(tx, d, p.Quote, state, reason, sum.String(), p.Revision, txID, v, s); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if changed {
		a.cryptoHub.publish(id)
	}
	return nil
}
func confirmationDelay(attempt int) int64 {
	delays := []int64{10, 20, 40, 80, 160, 300, 600, 1200, 1800, 3600}
	return delays[min(attempt, len(delays)-1)]
}
func (a *App) queueCryptoEvent(tx *sql.Tx, d Donation, q chain.Quote, state, reason, received string, revision int64, txID string, v chain.Evidence, s Settings) error {
	kind := state
	if kind == "overpaid" {
		kind = "paid"
	}
	if kind == "invalid" {
		kind = "failed"
	}
	event := notify.Event{ID: "crypto_" + d.ID + "_" + strconv.FormatInt(revision, 10), Type: "payment." + kind, Payment: map[string]any{"id": d.ID, "status": state, "reason": reason, "network": q.Network.ID, "asset": q.Token.Asset, "token_contract": q.Token.Contract, "recipient": q.Recipient, "expected_units": q.Expected, "received_units": received, "decimals": q.Token.Decimals, "expected": chain.Decimal(q.Expected, q.Token.Decimals), "received": chain.Decimal(received, q.Token.Decimals), "tx_id": txID, "transaction": v, "project_id": d.ProjectID}}
	return a.Notify.Queue(tx, event, notify.Config{Webhook: s.Webhook})
}
func (a *App) recheckCrypto(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) {
		fail(w, 403, "请求来源无效")
		return
	}
	if !a.rateLimit(r) {
		fail(w, 429, "请求过于频繁")
		return
	}
	var in struct {
		Token string `json:"status_token"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "请求无效")
		return
	}
	d, e := a.authorizeCrypto(r, in.Token)
	if e != nil {
		fail(w, 404, "记录不存在或访问令牌无效")
		return
	}
	unlock := a.lockCheckout("crypto_" + d.ID)
	defer unlock()
	rows, e := a.DB.Query("SELECT tx_id FROM crypto_transactions WHERE donation_id=? AND state IN ('submitted','confirming')", d.ID)
	if e != nil {
		internalError(w)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		internalError(w)
		return
	}
	for _, id := range ids {
		if e = a.verifyCrypto(r.Context(), d.ID, id); e != nil {
			internalError(w)
			return
		}
	}
	a.cryptoReceiptResponse(w, d.ID)
}
func (a *App) cryptoQR(w http.ResponseWriter, r *http.Request) {
	d, e := a.authorizeCrypto(r, r.URL.Query().Get("token"))
	if e != nil {
		fail(w, 404, "记录不存在")
		return
	}
	p, e := readCrypto(a.DB, d.ID)
	if e != nil {
		internalError(w)
		return
	}
	received, _ := chain.Units(p.Received)
	expected, _ := chain.Units(p.Quote.Expected)
	remaining := new(big.Int).Sub(expected, received)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	payload := chain.PaymentURI(p.Quote, remaining.String())
	if r.URL.Query().Get("address") == "1" {
		payload = p.Quote.Recipient
	}
	png, e := qrcode.Encode(payload, qrcode.Medium, 320)
	if e != nil {
		internalError(w)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}
func (a *App) adminCrypto(w http.ResponseWriter, r *http.Request) {
	v, e := a.cryptoView(r.PathValue("id"))
	if e != nil {
		fail(w, 404, "链上订单不存在")
		return
	}
	respond(w, 200, v)
}

// Signed provider events are wake-up hints. Their claimed amount/status is
// ignored. The configured chain RPC remains the only source of payment truth.
func (a *App) chainHook(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	if s.Crypto.EventSecret == "" {
		fail(w, 403, "链事件接收未配置")
		return
	}
	timestamp := r.Header.Get("X-Donate-Timestamp")
	stamp, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil || stamp < time.Now().Unix()-300 || stamp > time.Now().Unix()+300 {
		fail(w, 401, "事件时间戳无效")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	body, e := readBody(r)
	if e != nil {
		fail(w, 400, "事件过大")
		return
	}
	mac := hmac.New(sha256.New, []byte(s.Crypto.EventSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	signature, e := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Donate-Signature"), "sha256="))
	if e != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		fail(w, 401, "事件签名无效")
		return
	}
	var in struct {
		EventID string `json:"event_id"`
		Network string `json:"network"`
		TxID    string `json:"tx_id"`
	}
	if json.Unmarshal(body, &in) != nil || !validID(in.EventID) || !validID(in.Network) {
		fail(w, 400, "事件无效")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	result, e := tx.Exec("INSERT INTO crypto_hook_events(event_id,created_at) VALUES(?,?) ON CONFLICT(event_id) DO NOTHING", in.EventID, time.Now().Unix())
	if e != nil {
		internalError(w)
		return
	}
	count, _ := result.RowsAffected()
	if count > 0 {
		if in.TxID != "" {
			_, e = tx.Exec("UPDATE crypto_transactions SET next_check=? WHERE network=? AND tx_id=? AND state IN ('submitted','confirming')", time.Now().Unix(), in.Network, in.TxID)
		} else {
			_, e = tx.Exec("UPDATE crypto_transactions SET next_check=? WHERE network=? AND state IN ('submitted','confirming')", time.Now().Unix(), in.Network)
		}
		if e != nil {
			internalError(w)
			return
		}
	}
	if _, e = tx.Exec("DELETE FROM crypto_hook_events WHERE created_at<?", time.Now().Add(-30*24*time.Hour).Unix()); e != nil {
		internalError(w)
		return
	}
	if e = tx.Commit(); e != nil {
		internalError(w)
		return
	}
	a.wakeCrypto()
	respond(w, 200, map[string]bool{"received": true})
}
