package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

func (a *App) catalogMethod(w http.ResponseWriter, id string) (payments.Method, bool) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return payments.Method{}, false
	}
	m, ok := methodByID(s, id)
	if !ok || m.Type != "waffo" {
		fail(w, 400, "请先保存 Waffo 支付方式和商户凭据")
		return m, false
	}
	return m, true
}
func (a *App) waffoStores(w http.ResponseWriter, r *http.Request) {
	m, ok := a.catalogMethod(w, r.URL.Query().Get("method_id"))
	if !ok {
		return
	}
	stores, e := a.Payments.WaffoStores(r.Context(), m)
	if e != nil {
		fail(w, 502, payments.SafeError(e))
		return
	}
	respond(w, 200, map[string]any{"stores": stores})
}
func (a *App) waffoProducts(w http.ResponseWriter, r *http.Request) {
	m, ok := a.catalogMethod(w, r.URL.Query().Get("method_id"))
	if !ok {
		return
	}
	products, e := a.Payments.WaffoProducts(r.Context(), m, r.URL.Query().Get("store_id"))
	if e != nil {
		fail(w, 502, payments.SafeError(e))
		return
	}
	respond(w, 200, map[string]any{"products": products})
}
func (a *App) waffoMutate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID string `json:"method_id"`
		payments.WaffoProductInput
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !validID(key) || len(key) < 20 {
		fail(w, 400, "产品操作必须携带 20 到 80 位唯一 Idempotency-Key")
		return
	}
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	m, ok := a.catalogMethod(w, in.MethodID)
	if !ok {
		return
	}
	if in.StoreID == "" {
		in.StoreID = m.Config["store_id"]
	}
	if r.Method == http.MethodPut && len(in.Prices) == 0 {
		fail(w, 400, "更新产品需要完整 prices 价格表，以保留其他币种并保证重试使用相同内容")
		return
	}
	raw, _ := json.Marshal(in)
	requestHash := digest(r.Method + " " + r.URL.Path + " " + m.Config["merchant_id"] + " " + m.Config["environment"] + " " + string(raw))
	dbKey := "catalog_" + key
	var hash, status, response, createdAt string
	var partial payments.WaffoProduct
	e := a.DB.QueryRow("SELECT request_hash,status,response,created_at FROM idempotency WHERE key=?", dbKey).Scan(&hash, &status, &response, &createdAt)
	if e != nil && e != sql.ErrNoRows {
		internalError(w)
		return
	}
	if e == nil {
		if hash != requestHash {
			fail(w, 409, "同一个操作标识不能用于其他产品操作")
			return
		}
		if status == "complete" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(response))
			return
		}
		if response != "" {
			var previous struct {
				Product payments.WaffoProduct `json:"product"`
			}
			if json.Unmarshal([]byte(response), &previous) != nil {
				internalError(w)
				return
			}
			partial = previous.Product
		}
		created, parseErr := time.Parse(time.RFC3339Nano, createdAt)
		if partial.ID == "" && (parseErr != nil || time.Since(created) >= 23*time.Hour) {
			fail(w, 409, "此操作已超出支付平台的幂等重试期限。请先在 Waffo 核对已有产品，再发起新的操作。")
			return
		}
	} else {
		if _, e = a.DB.Exec("INSERT INTO idempotency(key,request_hash,status,created_at) VALUES(?,?,'processing',?)", dbKey, requestHash, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
			internalError(w)
			return
		}
	}
	var product payments.WaffoProduct
	if partial.ID != "" && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
		operation := "create"
		if r.Method == http.MethodPut {
			operation = "update"
		}
		product, e = a.Payments.WaffoResumeProductChange(r.Context(), m, partial, in.WaffoProductInput, operation, key)
	} else {
		switch r.Method {
		case http.MethodPost:
			product, e = a.Payments.WaffoCreateProduct(r.Context(), m, in.WaffoProductInput, key)
		case http.MethodPut:
			product, e = a.Payments.WaffoUpdateProduct(r.Context(), m, r.PathValue("id"), in.WaffoProductInput, key)
		case http.MethodDelete:
			product, e = a.Payments.WaffoDeleteProduct(r.Context(), m, r.PathValue("id"), key)
		}
	}
	if e != nil {
		if product.ID != "" {
			encoded, marshalErr := json.Marshal(map[string]any{"product": product})
			if marshalErr != nil {
				internalError(w)
				return
			}
			if _, err := a.DB.Exec("UPDATE idempotency SET status='retryable',response=? WHERE key=?", string(encoded), dbKey); err != nil {
				internalError(w)
				return
			}
		} else {
			_, _ = a.DB.Exec("UPDATE idempotency SET status='retryable' WHERE key=?", dbKey)
		}
		fail(w, 502, payments.SafeError(e))
		return
	}
	result := map[string]any{"product": product}
	encoded, e := json.Marshal(result)
	if e != nil {
		internalError(w)
		return
	}
	if _, e = a.DB.Exec("UPDATE idempotency SET status='complete',response=? WHERE key=?", string(encoded), dbKey); e != nil {
		internalError(w)
		return
	}
	respond(w, 200, result)
}
