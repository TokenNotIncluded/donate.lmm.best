package app

import "net/http"

type CurrencyTotal struct {
	Currency   string `json:"currency"`
	Count      int    `json:"count"`
	TotalMinor int64  `json:"total_minor"`
}
type MethodTotal struct {
	MethodID   string `json:"method_id"`
	MethodName string `json:"method_name"`
	Currency   string `json:"currency"`
	Count      int    `json:"count"`
	TotalMinor int64  `json:"total_minor"`
}
type DailyTotal struct {
	Date       string `json:"date"`
	Currency   string `json:"currency"`
	Count      int    `json:"count"`
	TotalMinor int64  `json:"total_minor"`
}
type Stats struct {
	Count      int             `json:"count"`
	TotalMinor int64           `json:"total_minor"`
	Currency   string          `json:"currency"`
	ByCurrency []CurrencyTotal `json:"by_currency"`
	Methods    []MethodTotal   `json:"methods"`
	Daily      []DailyTotal    `json:"daily"`
}

func (a *App) stats(currency string) (Stats, error) {
	tx, beginErr := a.DB.Begin()
	if beginErr != nil {
		return Stats{}, beginErr
	}
	defer tx.Rollback()
	result := Stats{Currency: currency, ByCurrency: []CurrencyTotal{}, Methods: []MethodTotal{}, Daily: []DailyTotal{}}
	if e := tx.QueryRow("SELECT count(*),coalesce(sum(amount_minor),0) FROM donations WHERE status='confirmed' AND currency=?", currency).Scan(&result.Count, &result.TotalMinor); e != nil {
		return result, e
	}
	rows, e := tx.Query("SELECT currency,count(*),sum(amount_minor) FROM donations WHERE status='confirmed' GROUP BY currency ORDER BY currency")
	if e != nil {
		return result, e
	}
	for rows.Next() {
		var c CurrencyTotal
		if e = rows.Scan(&c.Currency, &c.Count, &c.TotalMinor); e != nil {
			rows.Close()
			return result, e
		}
		result.ByCurrency = append(result.ByCurrency, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	rows, e = tx.Query("SELECT method_id,method_name,currency,count(*),sum(amount_minor) FROM donations WHERE status='confirmed' GROUP BY method_id,method_name,currency ORDER BY sum(amount_minor) DESC")
	if e != nil {
		return result, e
	}
	for rows.Next() {
		var c MethodTotal
		if e = rows.Scan(&c.MethodID, &c.MethodName, &c.Currency, &c.Count, &c.TotalMinor); e != nil {
			rows.Close()
			return result, e
		}
		result.Methods = append(result.Methods, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	rows, e = tx.Query("SELECT substr(paid_at,1,10),currency,count(*),sum(amount_minor) FROM donations WHERE status='confirmed' AND paid_at>=date('now','-90 days') GROUP BY substr(paid_at,1,10),currency ORDER BY substr(paid_at,1,10)")
	if e != nil {
		return result, e
	}
	for rows.Next() {
		var c DailyTotal
		if e = rows.Scan(&c.Date, &c.Currency, &c.Count, &c.TotalMinor); e != nil {
			rows.Close()
			return result, e
		}
		result.Daily = append(result.Daily, c)
	}
	e = rows.Err()
	rows.Close()
	return result, e
}
func (a *App) site(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	rawSite := map[string]any{} // Marshal only the public site type, never settings.
	raw, e := marshalSite(s.Site)
	if e != nil {
		internalError(w)
		return
	}
	rawSite = raw
	methods := []map[string]any{}
	for _, m := range s.Methods {
		if m.Enabled {
			methods = append(methods, map[string]any{"id": m.ID, "type": m.Type, "name": m.Name, "description": m.Description, "qr_url": m.QRURL, "checkout_url": m.CheckoutURL})
		}
	}
	rawSite["methods"] = methods
	st, e := a.stats(s.Site.Currency)
	if e != nil {
		internalError(w)
		return
	}
	rawSite["stats"] = st
	rows, e := a.DB.Query("SELECT name,amount_minor,currency,method_name,paid_at,message FROM donations WHERE status='confirmed' AND public=1 ORDER BY paid_at DESC LIMIT 12")
	if e != nil {
		internalError(w)
		return
	}
	defer rows.Close()
	recent := []map[string]any{}
	for rows.Next() {
		var name, currency, method, paidAt, message string
		var amount int64
		if e = rows.Scan(&name, &amount, &currency, &method, &paidAt, &message); e != nil {
			internalError(w)
			return
		}
		recent = append(recent, map[string]any{"name": name, "amount_minor": amount, "currency": currency, "method": method, "paid_at": paidAt, "message": message})
	}
	if rows.Err() != nil {
		internalError(w)
		return
	}
	rawSite["recent"] = recent
	respond(w, 200, rawSite)
}
func (a *App) publicStats(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	currency := r.URL.Query().Get("currency")
	if currency == "" {
		currency = s.Site.Currency
	}
	if _, ok := supportedCurrencies[currency]; !ok {
		fail(w, 400, "币种无效")
		return
	}
	st, e := a.stats(currency)
	if e != nil {
		internalError(w)
		return
	}
	respond(w, 200, st)
}
func (a *App) privateStats(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings()
	if e != nil {
		internalError(w)
		return
	}
	if !a.authorizedToken(r, s) {
		fail(w, 401, "统计令牌无效")
		return
	}
	a.publicStats(w, r)
}
