package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/chain"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type receiptHub struct {
	mu      sync.Mutex
	clients map[string]map[chan struct{}]bool
	count   int
}

func (h *receiptHub) subscribe(id string) (chan struct{}, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count >= 256 {
		return nil, nil, errors.New("too many streams")
	}
	if h.clients == nil {
		h.clients = map[string]map[chan struct{}]bool{}
	}
	if h.clients[id] == nil {
		h.clients[id] = map[chan struct{}]bool{}
	}
	ch := make(chan struct{}, 1)
	h.clients[id][ch] = true
	h.count++
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.clients[id], ch)
		h.count--
		if len(h.clients[id]) == 0 {
			delete(h.clients, id)
		}
	}, nil
}
func (h *receiptHub) publish(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (a *App) receiptEvents(w http.ResponseWriter, r *http.Request) {
	d, e := a.authorizeCrypto(r, r.URL.Query().Get("token"))
	if e != nil {
		fail(w, 404, "记录不存在或访问令牌无效")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 503, "实时连接不可用")
		return
	}
	ch, unsubscribe, e := a.cryptoHub.subscribe(d.ID)
	if e != nil {
		fail(w, 429, "实时连接过多")
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func() bool {
		current, e := a.donation(d.ID)
		if e != nil {
			return false
		}
		view, e := a.cryptoView(d.ID)
		if e != nil {
			return false
		}
		snapshot := receiptStatus(current)
		snapshot["crypto"] = view
		snapshot["can_cancel"] = false
		raw, e := json.Marshal(snapshot)
		if e != nil {
			return false
		}
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, e = fmt.Fprintf(w, "event: receipt\ndata: %s\n\n", raw)
		flusher.Flush()
		return e == nil
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send() {
				return
			}
		case <-ticker.C:
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, e = fmt.Fprint(w, ": keepalive\n\n"); e != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (a *App) wakeCrypto() {
	select {
	case a.cryptoWake <- struct{}{}:
	default:
	}
}

func (a *App) runCrypto(ctx context.Context) {
	workers := map[string]context.CancelFunc{}
	var wg sync.WaitGroup
	defer func() {
		for _, cancel := range workers {
			cancel()
		}
		wg.Wait()
	}()
	// Reconnect/startup: reverify only persisted waiting transaction IDs.
	a.DB.ExecContext(ctx, "UPDATE crypto_transactions SET next_check=? WHERE state IN ('submitted','confirming')", time.Now().Unix())
	for {
		if ctx.Err() != nil {
			return
		}
		// Quotes preserve old networks even after an operator retires them.
		rows, e := a.DB.QueryContext(ctx, `SELECT DISTINCT p.quote FROM crypto_payments p JOIN crypto_transactions t ON t.donation_id=p.donation_id WHERE t.state IN ('submitted','confirming')`)
		quotes := []chain.Quote{}
		if e == nil {
			for rows.Next() {
				var raw string
				var q chain.Quote
				if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &q) == nil {
					quotes = append(quotes, q)
				}
			}
			rows.Close()
		}
		active := map[string]bool{}
		for _, q := range quotes {
			n := q.Network
			if n.WSURL == "" {
				continue
			}
			key := digest(n.ID + "\n" + n.WSURL)
			active[key] = true
			if _, ok := workers[key]; !ok {
				watchCtx, cancel := context.WithCancel(ctx)
				workers[key] = cancel
				wg.Add(1)
				go func() { defer wg.Done(); a.watchChain(watchCtx, n) }()
			}
		}
		for key, cancel := range workers {
			if !active[key] {
				cancel()
				delete(workers, key)
			}
		}
		var id, txID string
		e = a.DB.QueryRowContext(ctx, "SELECT donation_id,tx_id FROM crypto_transactions WHERE state IN ('submitted','confirming') AND next_check<=? ORDER BY next_check LIMIT 1", time.Now().Unix()).Scan(&id, &txID)
		if e == nil {
			unlock := a.lockCheckout("crypto_" + id)
			err := a.verifyCrypto(ctx, id, txID)
			unlock()
			if err != nil {
				a.DB.ExecContext(ctx, "UPDATE crypto_transactions SET next_check=? WHERE donation_id=? AND tx_id=?", time.Now().Add(time.Minute).Unix(), id, txID)
			}
			continue
		}
		if a.expireCrypto(ctx) == nil { /* notifications and receipt push are emitted transactionally */
		}
		var next int64
		a.DB.QueryRowContext(ctx, `SELECT coalesce(min(due),0) FROM (
   SELECT next_check AS due FROM crypto_transactions WHERE state IN ('submitted','confirming')
   UNION ALL SELECT cast(strftime('%s',d.expires_at) AS INTEGER) AS due FROM crypto_payments p JOIN donations d ON d.id=p.donation_id WHERE p.state='open'
   UNION ALL SELECT p.deadline AS due FROM crypto_payments p WHERE p.state IN ('invalid','failed','partial','expired') AND p.reason<>'confirmation_timeout')`).Scan(&next)
		delay := 24 * time.Hour
		if next > 0 {
			delay = time.Until(time.Unix(next, 0))
			if delay < time.Second {
				delay = time.Second
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.cryptoWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (a *App) expireCrypto(ctx context.Context) error {
	rows, e := a.DB.QueryContext(ctx, `SELECT p.donation_id FROM crypto_payments p JOIN donations d ON d.id=p.donation_id WHERE (p.state='open' AND cast(strftime('%s',d.expires_at) AS INTEGER)<=?) OR (p.state IN ('invalid','failed','partial','expired') AND p.reason<>'confirmation_timeout' AND p.deadline<=?)`, time.Now().Unix(), time.Now().Unix())
	if e != nil {
		return e
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
		return e
	}
	for _, id := range ids {
		unlock := a.lockCheckout("crypto_" + id)
		e = a.expireCryptoOne(ctx, id)
		unlock()
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *App) expireCryptoOne(ctx context.Context, id string) error {
	p, e := readCrypto(a.DB, id)
	if e != nil {
		return e
	}
	d, e := a.donation(id)
	if e != nil {
		return e
	}
	if p.State == "paid" || p.State == "overpaid" || p.State == "submitted" || p.State == "confirming" {
		return nil
	}
	state, reason := "expired", "payment_expired"
	if p.Deadline <= time.Now().Unix() {
		state = "failed"
		reason = "confirmation_timeout"
	}
	if p.State == state && p.Reason == reason {
		return nil
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
	p.Revision++
	if _, e = tx.Exec("UPDATE crypto_payments SET state=?,reason=?,revision=? WHERE donation_id=?", state, reason, p.Revision, id); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE donations SET status='expired' WHERE id=? AND status='pending'", id); e != nil {
		return e
	}
	if e = a.queueCryptoEvent(tx, d, p.Quote, state, reason, p.Received, p.Revision, "", chain.Evidence{}, s); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	a.cryptoHub.publish(id)
	return nil
}
func (a *App) watchChain(ctx context.Context, n chain.Network) {
	delay := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if a.watchConnection(ctx, n) == nil {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
func (a *App) watchConnection(ctx context.Context, n chain.Network) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	client := *a.Chain.Client
	client.Timeout = 0
	headers := http.Header{}
	if n.APIKey != "" {
		headers.Set("TRON-PRO-API-KEY", n.APIKey)
	}
	c, _, e := websocket.Dial(dialCtx, n.WSURL, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: headers})
	cancel()
	if e != nil {
		return errors.New("stream_unavailable")
	}
	defer c.CloseNow()
	c.SetReadLimit(65536)
	method := "eth_subscribe"
	params := []any{"newHeads"}
	if n.Family == "solana" {
		method = "rootSubscribe"
		params = []any{}
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	e = wsjson.Write(writeCtx, c, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	cancel()
	if e != nil {
		return e
	}
	var ack struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e = wsjson.Read(readCtx, c, &ack)
	cancel()
	if e != nil || len(ack.Error) > 0 || len(ack.Result) == 0 {
		return errors.New("stream_unavailable")
	}
	// A fresh subscription covers missed blocks by verifying each known hash.
	a.chainWake(ctx, n.ID)
	last := time.Time{}
	for {
		var event struct {
			Method string `json:"method"`
		}
		readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		e = wsjson.Read(readCtx, c, &event)
		cancel()
		if e != nil {
			return e
		}
		if event.Method != "eth_subscription" && event.Method != "rootNotification" {
			continue
		}
		if time.Since(last) >= 10*time.Second {
			a.chainWake(ctx, n.ID)
			last = time.Now()
		}
	}
}
func (a *App) chainWake(ctx context.Context, network string) {
	// Unknown hashes retain their bounded backoff. Block events accelerate only
	// transactions already observed on-chain, protecting public RPC quotas.
	a.DB.ExecContext(ctx, "UPDATE crypto_transactions SET next_check=? WHERE network=? AND state='confirming'", time.Now().Unix(), network)
	a.wakeCrypto()
}
