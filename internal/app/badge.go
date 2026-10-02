package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type badgeOptions struct {
	Currency, Lang, Period, Layout, Theme, Title, AmountLabel, CountLabel, Animation string
	Width                                                                            int
}

func badgeParameter(q url.Values, key, fallback string) string {
	if value := q.Get(key); value != "" {
		return value
	}
	return fallback
}

func badgeText(value string, max int) (string, error) {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return "", errors.New("徽章文本长度或编码无效")
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", errors.New("徽章文本不能包含换行或控制字符")
		}
	}
	return strings.TrimSpace(value), nil
}

func parseBadgeOptions(raw string, site Site) (badgeOptions, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return badgeOptions{}, errors.New("徽章参数无效")
	}
	for _, key := range []string{"project", "currency", "lang", "period", "layout", "theme", "width", "title", "amount_label", "count_label", "animation"} {
		if len(q[key]) > 1 {
			return badgeOptions{}, errors.New("徽章参数不能重复")
		}
	}
	o := badgeOptions{Currency: badgeParameter(q, "currency", site.Currency), Lang: badgeParameter(q, "lang", "en"), Period: badgeParameter(q, "period", "all"), Layout: badgeParameter(q, "layout", "receipt"), Theme: badgeParameter(q, "theme", "dark"), Animation: badgeParameter(q, "animation", "none")}
	if o.Lang == "zh" {
		o.Lang = "zh-CN"
	}
	if _, ok := supportedCurrencies[o.Currency]; !ok || !supportedLanguages[o.Lang] || !contains([]string{"all", "7d", "30d", "year"}, o.Period) || !contains([]string{"receipt", "compact"}, o.Layout) || !contains([]string{"dark", "light", "transparent"}, o.Theme) || !contains([]string{"none", "steam"}, o.Animation) {
		return o, errors.New("徽章币种、语言或样式参数无效")
	}
	o.Width = 480
	if o.Layout == "compact" {
		o.Width = 440
	}
	if q.Get("width") != "" {
		o.Width, err = strconv.Atoi(q.Get("width"))
		if err != nil || o.Width < 240 || o.Width > 1200 {
			return o, errors.New("徽章宽度需为 240 到 1200 的整数")
		}
	}
	labels := map[string][2]string{"en": {"Donation amount", "Donations"}, "zh-CN": {"捐赠金额", "捐赠笔数"}, "zh-TW": {"捐贈金額", "捐贈筆數"}}[o.Lang]
	for _, text := range []struct {
		key, fallback string
		max           int
		out           *string
	}{{"title", "Donate", 40, &o.Title}, {"amount_label", labels[0], 24, &o.AmountLabel}, {"count_label", labels[1], 24, &o.CountLabel}} {
		value, err := badgeText(badgeParameter(q, text.key, text.fallback), text.max)
		if err != nil {
			return o, err
		}
		if value == "" {
			value = text.fallback
		}
		*text.out = value
	}
	return o, nil
}

func badgePeriod(period, lang string, now time.Time) (time.Time, string) {
	now = now.UTC()
	var start time.Time
	labels := map[string][3]string{"en": {"All time", "Last 7 days · UTC", "Last 30 days · UTC"}, "zh-CN": {"全部捐赠", "最近 7 天 · UTC", "最近 30 天 · UTC"}, "zh-TW": {"全部捐贈", "最近 7 天 · UTC", "最近 30 天 · UTC"}}[lang]
	label := labels[0]
	switch period {
	case "7d":
		start, label = now.Add(-7*24*time.Hour), labels[1]
	case "30d":
		start, label = now.Add(-30*24*time.Hour), labels[2]
	case "year":
		start = time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		label = strconv.Itoa(now.Year()) + " · UTC"
	}
	return start, label
}

// Application writers persist UTC RFC3339Nano dates. Padding the optional
// fraction preserves exact ordering at nanosecond boundaries without floating
// point conversions or SQLite julianday's millisecond rounding.
const badgePaidTime = "substr(paid_at,1,19) || '.' || substr(CASE WHEN substr(paid_at,20,1)='.' THEN substr(paid_at,21,length(paid_at)-21) ELSE '' END || '000000000',1,9) || 'Z'"
const badgeTimeFormat = "2006-01-02T15:04:05.000000000Z"

func (a *App) publicBadge(w http.ResponseWriter, r *http.Request) {
	a.publicBadgeAt(w, r, time.Now().UTC())
}

func (a *App) publicBadgeAt(w http.ResponseWriter, r *http.Request, now time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	s, err := a.settings()
	if err != nil {
		internalError(w)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["project"]) > 1 {
		fail(w, http.StatusBadRequest, "徽章项目参数无效或重复")
		return
	}
	var project *Project
	if ids, present := query["project"]; present {
		if len(ids) != 1 || !validProjectID(ids[0]) {
			fail(w, http.StatusBadRequest, "徽章项目参数无效")
			return
		}
		p, lookupErr := a.publicProjectAt(r.Context(), ids[0], now)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "项目不存在")
			return
		}
		if lookupErr != nil {
			internalError(w)
			return
		}
		project = &p
		s.Site.Currency = p.Currency
	}
	o, err := parseBadgeOptions(r.URL.RawQuery, s.Site)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if project != nil {
		_, currencyProvided := query["currency"]
		_, periodProvided := query["period"]
		if o.Currency != project.Currency || o.Period != "all" || (currencyProvided && query.Get("currency") != project.Currency) || (periodProvided && query.Get("period") != "all") {
			fail(w, http.StatusBadRequest, "项目徽章需使用项目币种和全部捐赠")
			return
		}
		if strings.TrimSpace(query.Get("title")) == "" {
			o.Title = project.Name
		}
		a.writeBadge(w, r, renderProjectBadge(o, *project))
		return
	}
	start, periodLabel := badgePeriod(o.Period, o.Lang, now)
	where := "status='confirmed' AND currency=? AND paid_at GLOB '????-??-??T??:??:??*Z' AND (" + badgePaidTime + ")<=?"
	args := []any{o.Currency, now.UTC().Format(badgeTimeFormat)}
	if !start.IsZero() {
		where += " AND (" + badgePaidTime + ")>=?"
		args = append(args, start.Format(badgeTimeFormat))
	}
	var total, count int64
	if err = a.DB.QueryRowContext(r.Context(), "SELECT coalesce(sum(amount_minor),0),count(*) FROM donations WHERE "+where, args...).Scan(&total, &count); err != nil {
		internalError(w)
		return
	}
	a.writeBadge(w, r, renderBadge(o, total, count, periodLabel))
}

func (a *App) writeBadge(w http.ResponseWriter, r *http.Request, content []byte) {
	sum := sha256.Sum256(content)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}

func badgeGrouped(value int64) string {
	raw := strconv.FormatInt(value, 10)
	for i := len(raw) - 3; i > 0; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	return raw
}

func badgeAmount(value int64, currency string) string {
	factor := supportedCurrencies[currency]
	amount := badgeGrouped(value / factor)
	if factor == 100 {
		amount += fmt.Sprintf(".%02d", value%factor)
	}
	return currency + " " + amount
}

func badgeXML(text string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
}

func badgeTextWidth(text string, font float64) float64 {
	width := 0.0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r == ' ':
			width += font * .34
		case r < 128:
			width += font * .64
		default:
			width += font
		}
	}
	return width
}

func badgeWrap(text string, width, font float64) []string {
	lines, line := []string{}, ""
	for _, r := range text {
		if line != "" && badgeTextWidth(line+string(r), font) > width {
			lines, line = append(lines, line), ""
		}
		line += string(r)
	}
	return append(lines, line)
}

func badgeFit(text string, width, font float64) float64 {
	if actual := badgeTextWidth(text, font); actual > width {
		return font * width / actual
	}
	return font
}

func renderBadge(o badgeOptions, total, count int64, period string) []byte {
	return renderBadgeWithProject(o, total, count, period, nil)
}

func renderProjectBadge(o badgeOptions, project Project) []byte {
	_, period := badgePeriod("all", o.Lang, time.Time{})
	return renderBadgeWithProject(o, project.RaisedMinor, project.Count, period, &project)
}

func renderBadgeWithProject(o badgeOptions, total, count int64, period string, project *Project) []byte {
	baseWidth, pad, titleFont := 480, 24.0, 20.0
	if o.Layout == "compact" {
		baseWidth, pad, titleFont = 440, 22, 18
	}
	bg, ink, muted, line := "#0b0b0b", "#f5f5f5", "#a3a3a3", "#3f3f3f"
	if o.Theme != "dark" {
		bg, ink, muted, line = "#ffffff", "#111111", "#555555", "#cccccc"
	}
	contentWidth := float64(baseWidth) - 2*pad
	titles := badgeWrap(o.Title, contentWidth-96, titleFont)
	headerBottom := 20 + float64(len(titles))*24
	if headerBottom < 80 {
		headerBottom = 80
	}
	separator := headerBottom + 12
	amount, counted := badgeAmount(total, o.Currency), badgeGrouped(count)
	amountLabels := badgeWrap(o.AmountLabel, contentWidth, 12)
	countFont := 24.0
	countLabels := badgeWrap(o.CountLabel, contentWidth-badgeTextWidth(counted, countFont)-12, 12)
	amountY := separator + 24 + float64(len(amountLabels)-1)*16 + 47
	countY := amountY + 57
	periodY := countY + float64(len(countLabels)-1)*16 + 34
	if o.Layout == "compact" {
		amountWidth, countWidth := (contentWidth-24)/2, (contentWidth-24)/2
		if project != nil {
			// Project goals need room for two full-precision monetary values.
			// Keep the amount on its own line, even in the compact layout.
			amountWidth = contentWidth
			countWidth = contentWidth - badgeTextWidth(counted, countFont) - 12
		}
		amountLabels, countLabels = badgeWrap(o.AmountLabel, amountWidth, 11), badgeWrap(o.CountLabel, countWidth, 11)
		labelRows := len(amountLabels)
		if project == nil && len(countLabels) > labelRows {
			labelRows = len(countLabels)
		}
		amountY = separator + 22 + float64(labelRows-1)*15 + 30
		countY, periodY = amountY, amountY+30
	}
	var target, percentage string
	var targetY, progressY, progress float64
	if project != nil {
		target = "/ " + badgeAmount(project.TargetMinor, o.Currency)
		progress = project.Progress
		if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 {
			progress = 0
		}
		if progress > 100 {
			progress = 100
		}
		percentage = strconv.FormatFloat(progress, 'f', 1, 64) + "%"
		targetY, progressY = amountY+25, amountY+49
		if o.Layout == "receipt" {
			countY = progressY + 39
			periodY = countY + float64(len(countLabels)-1)*16 + 34
		} else {
			countY = progressY + 31
			periodY = countY + float64(len(countLabels)-1)*16 + 28
		}
	}
	baseHeight := int(periodY + 24)
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="badge-title badge-description">`, o.Width, (baseHeight*o.Width+baseWidth/2)/baseWidth, baseWidth, baseHeight)
	description := o.AmountLabel + ": " + amount + ". " + o.CountLabel + ": " + counted + ". " + period + "."
	if project != nil {
		description = project.Name + ". " + o.AmountLabel + ": " + amount + " / " + badgeAmount(project.TargetMinor, o.Currency) + ". " + percentage + ". " + o.CountLabel + ": " + counted + ". " + period + "."
	}
	fmt.Fprintf(&b, `<title id="badge-title">%s</title><desc id="badge-description">%s</desc>`, badgeXML(o.Title), badgeXML(description))
	if o.Theme != "transparent" {
		fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" fill="%s" stroke="%s"/>`, baseWidth-1, baseHeight-1, bg, line)
	}
	if o.Animation == "steam" {
		b.WriteString(`<style>.steam{animation:coffee-steam 2.4s ease-in-out infinite}@keyframes coffee-steam{0%,100%{opacity:.45;transform:translateY(1px)}50%{opacity:1;transform:translateY(-2px)}}@media(prefers-reduced-motion:reduce){.steam{animation:none;opacity:.75}}</style>`)
	}
	text := func(x, y, font float64, color, value string) {
		fmt.Fprintf(&b, `<text x="%.2f" y="%.2f" font-size="%.2f" fill="%s">%s</text>`, x, y, font, color, badgeXML(value))
	}
	b.WriteString(`<g font-family="ui-monospace,monospace">`)
	for i, title := range titles {
		text(pad, 42+float64(i)*24, titleFont, ink, title)
	}
	b.WriteString(`</g>`)
	coffeeX := float64(baseWidth) - pad - 76
	fmt.Fprintf(&b, `<g fill="%s" font-family="ui-monospace,monospace" font-size="9" xml:space="preserve"><g class="steam">`, muted)
	for i, art := range []string{"   )  (    ", "    (  )   ", "  .----.   ", "  |    |)  ", "  '----'   ", " --------  "} {
		if i == 2 {
			b.WriteString(`</g>`)
		}
		fmt.Fprintf(&b, `<text x="%.2f" y="%d">%s</text>`, coffeeX, 29+i*9, badgeXML(art))
	}
	b.WriteString(`</g>`)
	fmt.Fprintf(&b, `<path d="M%.0f %.0fH%.0f" fill="none" stroke="%s" stroke-dasharray="3 4"/>`, pad, separator, float64(baseWidth)-pad, line)
	b.WriteString(`<g font-family="ui-monospace,monospace" font-variant-numeric="tabular-nums">`)
	if o.Layout == "receipt" || project != nil {
		amountFont, amountLabelFont, countLabelFont, amountLabelY := 44.0, 12.0, 12.0, separator+24
		if o.Layout == "compact" {
			amountFont, amountLabelFont, countLabelFont, amountLabelY = 26, 11, 11, separator+22
		}
		for i, label := range amountLabels {
			text(pad, amountLabelY+float64(i)*16, amountLabelFont, muted, label)
		}
		text(pad, amountY, badgeFit(amount, contentWidth, amountFont), ink, amount)
		if project == nil {
			fmt.Fprintf(&b, `<path d="M%.0f %.0fH%.0f" fill="none" stroke="%s" stroke-dasharray="3 4"/>`, pad, amountY+23, float64(baseWidth)-pad, line)
		}
		text(pad, countY, countFont, ink, counted)
		for i, label := range countLabels {
			text(pad+badgeTextWidth(counted, countFont)+12, countY+float64(i)*16, countLabelFont, muted, label)
		}
	} else {
		col, right := (contentWidth-24)/2, float64(baseWidth)/2+12
		for i, label := range amountLabels {
			text(pad, separator+22+float64(i)*15, 11, muted, label)
		}
		for i, label := range countLabels {
			text(right, separator+22+float64(i)*15, 11, muted, label)
		}
		text(pad, amountY, badgeFit(amount, col, 26), ink, amount)
		text(right, countY, badgeFit(counted, col, 26), ink, counted)
	}
	if project != nil {
		text(pad, targetY, badgeFit(target, contentWidth-72, 12), muted, target)
		text(float64(baseWidth)-pad-badgeTextWidth(percentage, 12), targetY, 12, muted, percentage)
		fmt.Fprintf(&b, `<g role="progressbar" aria-label="%s" aria-valuemin="0" aria-valuemax="100" aria-valuenow="%.1f"><path d="M%.2f %.2fH%.2f" fill="none" stroke="%s" stroke-width="6"/><path d="M%.2f %.2fH%.2f" fill="none" stroke="%s" stroke-width="6"/></g>`, badgeXML(project.Name), progress, pad, progressY, float64(baseWidth)-pad, line, pad, progressY, pad+contentWidth*progress/100, ink)
	}
	text(pad, periodY, 12, muted, period)
	b.WriteString(`</g></svg>`)
	return b.Bytes()
}
