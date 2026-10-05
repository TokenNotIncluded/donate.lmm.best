package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type ProjectCurrencyTotal struct {
	Currency   string `json:"currency"`
	TotalMinor int64  `json:"total_minor"`
	Count      int64  `json:"count"`
}

type Project struct {
	ByCurrency  []ProjectCurrencyTotal `json:"by_currency"`
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	URL         string                 `json:"url"`
	Currency    string                 `json:"currency"`
	TargetMinor int64                  `json:"target_minor"`
	RaisedMinor int64                  `json:"raised_minor"`
	Count       int64                  `json:"count"`
	Progress    float64                `json:"progress"`
	Active      bool                   `json:"active"`
	DonateURL   string                 `json:"donate_url"`
	BadgeURL    string                 `json:"badge_url"`
	CreatedAt   string                 `json:"created_at"`
	UpdatedAt   string                 `json:"updated_at"`
}

type projectInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Currency    string `json:"currency"`
	TargetMinor int64  `json:"target_minor"`
	Active      *bool  `json:"active"`
}

var projectIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

func validProjectID(id string) bool { return projectIDPattern.MatchString(id) }

func projectFromInput(in projectInput, defaultActive bool) (Project, error) {
	p := Project{ID: in.ID, Name: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL), Currency: in.Currency, TargetMinor: in.TargetMinor, Active: defaultActive}
	if in.Active != nil {
		p.Active = *in.Active
	}
	if !validProjectID(p.ID) {
		return p, errors.New("项目标识需为 1 到 64 位小写字母、数字或中间的连字符")
	}
	if !utf8.ValidString(p.Name) || utf8.RuneCountInString(p.Name) < 1 || utf8.RuneCountInString(p.Name) > 80 {
		return p, errors.New("项目名称需为 1 到 80 个字符")
	}
	for _, r := range p.Name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return p, errors.New("项目名称不能包含控制字符")
		}
	}
	if _, ok := supportedCurrencies[p.Currency]; !ok {
		return p, errors.New("项目币种无效")
	}
	if p.TargetMinor <= 0 {
		return p, errors.New("目标金额应大于零")
	}
	if len(p.URL) > 2000 {
		return p, errors.New("项目链接过长")
	}
	if p.URL != "" {
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(p.URL, "\r\n\t") {
			return p, errors.New("项目链接需为 HTTPS 地址")
		}
	}
	return p, nil
}

const projectColumns = "id,name,url,currency,target_minor,active,created_at,updated_at"

type projectQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanProject(s scanner) (Project, error) {
	var p Project
	err := s.Scan(&p.ID, &p.Name, &p.URL, &p.Currency, &p.TargetMinor, &p.Active, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (a *App) projectLinks(p *Project) {
	p.DonateURL = a.PublicURL + "/?project=" + url.QueryEscape(p.ID)
	p.BadgeURL = a.PublicURL + "/badge.svg?project=" + url.QueryEscape(p.ID)
}

func ProjectProgress(raised, target int64) float64 {
	if raised <= 0 || target <= 0 {
		return 0
	}
	if raised >= target {
		return 100
	}
	return float64(raised) / float64(target) * 100
}

func (a *App) projectTotals(ctx context.Context, q projectQueryer, p *Project, now time.Time) error {
	p.ByCurrency = []ProjectCurrencyTotal{}
	totals := map[string]*ProjectCurrencyTotal{}
	rows, err := q.QueryContext(ctx, "SELECT currency,amount_minor,paid_at FROM donations WHERE project_id=? AND status='confirmed' ORDER BY currency", p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var amount int64
		var rawPaid, unit string
		if err = rows.Scan(&unit, &amount, &rawPaid); err != nil {
			return err
		}
		paid, parseErr := time.Parse(time.RFC3339Nano, rawPaid)
		if parseErr != nil || paid.After(now) || amount <= 0 {
			continue
		}
		total := totals[unit]
		if total == nil {
			total = &ProjectCurrencyTotal{Currency: unit}
			totals[unit] = total
		}
		// Never add unlike currencies, and keep sums safe from integer overflow.
		if amount > math.MaxInt64-total.TotalMinor {
			total.TotalMinor = math.MaxInt64
		} else {
			total.TotalMinor += amount
		}
		if total.Count < math.MaxInt64 {
			total.Count++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, unit := range []string{"USD", "EUR", "GBP", "CNY", "TWD", "HKD", "JPY"} {
		if total := totals[unit]; total != nil {
			p.ByCurrency = append(p.ByCurrency, *total)
		}
	}
	if total := totals[p.Currency]; total != nil {
		p.RaisedMinor = total.TotalMinor
		p.Count = total.Count
	}
	p.Progress = ProjectProgress(p.RaisedMinor, p.TargetMinor)
	a.projectLinks(p)
	return nil
}

func (a *App) projectAt(ctx context.Context, id string, now time.Time, activeOnly bool) (Project, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	query := "SELECT " + projectColumns + " FROM projects WHERE id=?"
	if activeOnly {
		query += " AND active=1"
	}
	p, err := scanProject(tx.QueryRowContext(ctx, query, id))
	if err != nil {
		return p, err
	}
	if err = a.projectTotals(ctx, tx, &p, now); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (a *App) project(ctx context.Context, id string) (Project, error) {
	return a.projectAt(ctx, id, time.Now(), false)
}
func (a *App) publicProject(ctx context.Context, id string) (Project, error) {
	return a.publicProjectAt(ctx, id, time.Now())
}
func (a *App) publicProjectAt(ctx context.Context, id string, now time.Time) (Project, error) {
	return a.projectAt(ctx, id, now, true)
}

func (a *App) projects(ctx context.Context, activeOnly bool) ([]Project, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := "SELECT " + projectColumns + " FROM projects"
	if activeOnly {
		query += " WHERE active=1"
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	items := []Project{}
	for rows.Next() {
		p, scanErr := scanProject(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range items {
		if err = a.projectTotals(ctx, tx, &items[i], now); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit()
}

func (a *App) listProjects(w http.ResponseWriter, r *http.Request)      { a.writeProjects(w, r, true) }
func (a *App) listAdminProjects(w http.ResponseWriter, r *http.Request) { a.writeProjects(w, r, false) }
func (a *App) writeProjects(w http.ResponseWriter, r *http.Request, activeOnly bool) {
	w.Header().Set("Cache-Control", "no-store")
	items, err := a.projects(r.Context(), activeOnly)
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{"projects": items})
}

func (a *App) publicProjectHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !validProjectID(r.PathValue("id")) {
		fail(w, http.StatusBadRequest, "项目标识无效")
		return
	}
	p, err := a.publicProject(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, "项目不存在")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, p)
}

func (a *App) createProject(w http.ResponseWriter, r *http.Request) {
	var in projectInput
	if err := decode(w, r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := projectFromInput(in, true)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !validID(key) || len(key) < 20 {
		fail(w, http.StatusBadRequest, "创建项目需携带 20 到 80 位唯一 Idempotency-Key")
		return
	}
	raw, _ := json.Marshal(struct {
		ID, Name, URL, Currency string
		TargetMinor             int64
		Active                  bool
	}{p.ID, p.Name, p.URL, p.Currency, p.TargetMinor, p.Active})
	hash := digest(string(raw))
	dbKey := "project_create_" + key
	a.checkoutMu.Lock()
	defer a.checkoutMu.Unlock()
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), "UPDATE idempotency SET key=key WHERE key=?", dbKey); err != nil {
		internalError(w)
		return
	}
	var storedHash, status, response string
	err = tx.QueryRowContext(r.Context(), "SELECT request_hash,status,response FROM idempotency WHERE key=?", dbKey).Scan(&storedHash, &status, &response)
	if err == nil {
		if storedHash != hash {
			fail(w, http.StatusConflict, "同一个操作标识不能用于不同的项目")
			return
		}
		if status != "complete" || response == "" {
			fail(w, http.StatusConflict, "项目创建尚未完成")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(response))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		internalError(w)
		return
	}
	var exists bool
	if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)", p.ID).Scan(&exists); err != nil {
		internalError(w)
		return
	}
	if exists {
		fail(w, http.StatusConflict, "项目标识已存在")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	p.CreatedAt = now
	p.UpdatedAt = now
	a.projectLinks(&p)
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO projects("+projectColumns+") VALUES(?,?,?,?,?,?,?,?)", p.ID, p.Name, p.URL, p.Currency, p.TargetMinor, p.Active, p.CreatedAt, p.UpdatedAt); err != nil {
		internalError(w)
		return
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		internalError(w)
		return
	}
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO idempotency(key,request_hash,status,response,created_at) VALUES(?,?,'complete',?,?)", dbKey, hash, string(encoded), now); err != nil {
		internalError(w)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusCreated, p)
}

func (a *App) updateProject(w http.ResponseWriter, r *http.Request) {
	var in projectInput
	if err := decode(w, r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	if in.ID != "" && in.ID != id {
		fail(w, http.StatusBadRequest, "项目标识创建后不能修改")
		return
	}
	in.ID = id
	if _, err := projectFromInput(in, true); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	a.checkoutMu.Lock()
	defer a.checkoutMu.Unlock()
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), "UPDATE projects SET updated_at=updated_at WHERE id=?", id); err != nil {
		internalError(w)
		return
	}
	previous, err := scanProject(tx.QueryRowContext(r.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, "项目不存在")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	p, _ := projectFromInput(in, previous.Active)
	if p.Currency != previous.Currency {
		var used bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM donations WHERE project_id=?)", id).Scan(&used); err != nil {
			internalError(w)
			return
		}
		if used {
			fail(w, http.StatusConflict, "已有捐赠记录的项目不能修改币种")
			return
		}
	}
	p.CreatedAt = previous.CreatedAt
	p.UpdatedAt = previous.UpdatedAt
	if p.Name != previous.Name || p.URL != previous.URL || p.Currency != previous.Currency || p.TargetMinor != previous.TargetMinor || p.Active != previous.Active {
		p.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(r.Context(), "UPDATE projects SET name=?,url=?,currency=?,target_minor=?,active=?,updated_at=? WHERE id=?", p.Name, p.URL, p.Currency, p.TargetMinor, p.Active, p.UpdatedAt, id); err != nil {
			internalError(w)
			return
		}
	}
	if err = a.projectTotals(r.Context(), tx, &p, time.Now()); err != nil {
		internalError(w)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w)
		return
	}
	respond(w, http.StatusOK, p)
}

func validateDonationProject(ctx context.Context, q projectQueryer, id, currency string) (string, error) {
	if id == "" {
		return "", nil
	}
	if !validProjectID(id) {
		return "", errors.New("项目标识无效")
	}
	var name string
	err := q.QueryRowContext(ctx, "SELECT name FROM projects WHERE id=? AND active=1", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("项目不存在或已归档")
	}
	if err != nil {
		return "", err
	}
	if _, ok := supportedCurrencies[currency]; !ok {
		return "", errors.New("捐赠币种无效")
	}
	return name, nil
}
