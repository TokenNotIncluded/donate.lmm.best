package app

import (
	"errors"
	"net/http"
	"strconv"
)

func (a *App) donorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/donor/session", a.Donors.Session)
	mux.HandleFunc("POST /api/donor/register/begin", a.Donors.RegisterBegin)
	mux.HandleFunc("POST /api/donor/register/finish", a.Donors.RegisterFinish)
	mux.HandleFunc("POST /api/donor/login/begin", a.Donors.LoginBegin)
	mux.HandleFunc("POST /api/donor/login/finish", a.Donors.LoginFinish)
	mux.HandleFunc("POST /api/donor/logout", a.Donors.Logout)
	mux.HandleFunc("GET /api/donor/donations", a.donorDonations)
}

// checkoutDonor associates only the verified donor session. Anonymous checkout
// keeps its original flow; identity is never accepted from donation JSON.
func (a *App) checkoutDonor(r *http.Request) (string, error) {
	user, err := a.Donors.Current(r)
	if err != nil || user == nil {
		return "", err
	}
	authorized, err := a.Donors.Authorize(r)
	if err != nil {
		return "", err
	}
	if authorized == nil || authorized.ID != user.ID {
		return "", errors.New("donor session changed")
	}
	return user.ID, nil
}

type donorDonation struct {
	ID           string `json:"id"`
	AmountMinor  int64  `json:"amount_minor"`
	Currency     string `json:"currency"`
	MethodName   string `json:"method_name"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	PaidAt       string `json:"paid_at"`
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	PublicThanks bool   `json:"public_thanks"`
}

func (a *App) donorDonations(w http.ResponseWriter, r *http.Request) {
	user, err := a.Donors.Current(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "请重新登录")
		return
	}
	if user == nil {
		fail(w, http.StatusUnauthorized, "请先登录")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRowContext(r.Context(), "SELECT count(*) FROM donations WHERE donor_user_id=? AND donor_user_id<>''", user.ID).Scan(&total); err != nil {
		internalError(w)
		return
	}
	rows, err := tx.QueryContext(r.Context(), "SELECT d.id,d.amount_minor,d.currency,d.method_name,d.status,d.created_at,d.paid_at,d.project_id,coalesce(p.name,''),d.public_thanks FROM donations d LEFT JOIN projects p ON p.id=d.project_id WHERE d.donor_user_id=? AND d.donor_user_id<>'' ORDER BY d.created_at DESC,d.id DESC LIMIT ? OFFSET ?", user.ID, limit, offset)
	if err != nil {
		internalError(w)
		return
	}
	defer rows.Close()
	items := []donorDonation{}
	for rows.Next() {
		var d donorDonation
		if err = rows.Scan(&d.ID, &d.AmountMinor, &d.Currency, &d.MethodName, &d.Status, &d.CreatedAt, &d.PaidAt, &d.ProjectID, &d.ProjectName, &d.PublicThanks); err != nil {
			internalError(w)
			return
		}
		items = append(items, d)
	}
	if rows.Err() != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"donations": items, "total": total, "limit": limit, "offset": offset})
}
