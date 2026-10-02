package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

type Translation struct {
	Tagline     string `json:"tagline"`
	Description string `json:"description"`
	Footer      string `json:"footer"`
	Terms       string `json:"terms"`
	Privacy     string `json:"privacy"`
}
type Site struct {
	Name               string                 `json:"name"`
	Tagline            string                 `json:"tagline"`
	Description        string                 `json:"description"`
	Footer             string                 `json:"footer"`
	Currency           string                 `json:"currency"`
	Presets            []int64                `json:"presets"`
	CollectName        bool                   `json:"collect_name"`
	CollectEmail       bool                   `json:"collect_email"`
	CollectMessage     bool                   `json:"collect_message"`
	ContactEmail       string                 `json:"contact_email"`
	Languages          []string               `json:"languages"`
	DefaultLanguage    string                 `json:"default_language"`
	Currencies         []string               `json:"currencies"`
	LanguageCurrencies map[string]string      `json:"language_currencies"`
	Terms              string                 `json:"terms"`
	Privacy            string                 `json:"privacy"`
	Translations       map[string]Translation `json:"translations"`
}
type Settings struct {
	Site       Site                 `json:"site"`
	Methods    []payments.Method    `json:"methods"`
	Webhook    notify.WebhookConfig `json:"webhook"`
	SMTP       notify.SMTPConfig    `json:"smtp"`
	StatsToken string               `json:"stats_token"`
}

func defaults() Settings {
	return Settings{
		Site: Site{Name: "Donate", Currency: "USD", Presets: []int64{5, 15, 50, 100}, CollectName: true, CollectEmail: true, CollectMessage: true, Languages: []string{"zh-CN", "zh-TW", "en"}, DefaultLanguage: "zh-CN", Currencies: []string{"USD", "CNY", "TWD", "EUR", "GBP", "HKD", "JPY"}, LanguageCurrencies: map[string]string{"zh-CN": "CNY", "zh-TW": "TWD", "en": "USD"}, Terms: "这是对开源项目的自愿支持，不构成购买商品、服务或取得权益的承诺。请确认金额和支付方式后再付款；手续费、汇率与支付规则以支付平台为准。退款或误付款请联系网站维护者，依法应享有的权利不受影响。请勿冒用他人信息或进行违法交易。", Privacy: "我们保存捐赠金额、币种、支付方式、时间，以及你自愿填写的名字、邮箱和留言，用于核对捐赠与通知维护者。银行卡等支付凭据由支付平台处理，本站不保存。只有你选择公开时，名字和留言才会显示在感谢列表；邮箱不会公开。可选账号保存账号标识、Passkey 公钥和关联捐款，设备生物识别数据不上传。用户和后台登录使用必要 Cookie，不使用广告追踪。你可联系维护者请求查阅、更正或删除信息；依法需留存的交易记录除外。", Translations: map[string]Translation{
			"en":    {Terms: "Your donation voluntarily supports an open-source project and does not promise goods, services, or other benefits. Check the amount and payment method before paying. Provider fees, exchange rates, and payment rules apply. Contact the maintainer about refunds or mistaken payments; your statutory rights remain unaffected. Do not misuse another person's information or make unlawful transactions.", Privacy: "We keep the amount, currency, payment method, time, and any name, email, or message you choose to provide to reconcile donations and notify the maintainer. Payment providers handle card details; we do not store them. Your name and message appear publicly only with your consent; email is never public. Optional accounts store an identifier, Passkey public keys and linked donations; biometric data stays on your device. User and admin sign-in use essential cookies, with no advertising trackers. Contact the maintainer to access, correct, or delete your information, subject to legally required transaction retention."},
			"zh-TW": {Terms: "這是對開源專案的自願支持，不構成購買商品、服務或取得權益的承諾。請確認金額與付款方式；手續費、匯率與支付規則以支付平台為準。退款或誤付款請聯絡維護者，依法應享有的權利不受影響。請勿冒用他人資訊或進行違法交易。", Privacy: "我們保存捐贈金額、幣別、付款方式、時間，以及你自願提供的名字、信箱和留言，用於核對與通知維護者。支付平台處理信用卡等付款資料，本站不保存。只有選擇公開時，名字和留言才會顯示；信箱不公開。選填帳號保存帳號識別碼、Passkey 公開金鑰及關聯捐款，裝置生物識別資料不上傳。使用者與後台登入使用必要 Cookie，不使用廣告追蹤。你可聯絡維護者請求查閱、更正或刪除資訊，依法需保留的交易紀錄除外。"},
		}},
		Methods: []payments.Method{}, StatsToken: randomToken(),
	}
}

var supportedCurrencies = map[string]int64{"USD": 100, "EUR": 100, "GBP": 100, "CNY": 100, "TWD": 100, "HKD": 100, "JPY": 1}
var supportedLanguages = map[string]bool{"en": true, "zh-CN": true, "zh-TW": true}

func contains(items []string, s string) bool {
	for _, v := range items {
		if v == s {
			return true
		}
	}
	return false
}
func validateSettings(s Settings) error {
	if strings.TrimSpace(s.Site.Name) == "" || len(s.Site.Name) > 120 {
		return errors.New("网站名称不能为空，且不能超过 120 字节")
	}
	if len(s.Site.Description) > 10000 || len(s.Site.Terms) > 20000 || len(s.Site.Privacy) > 20000 || len(s.Site.Footer) > 2000 || len(s.Site.Tagline) > 500 {
		return errors.New("网站文本过长")
	}
	if len(s.Site.Currencies) == 0 || len(s.Site.Currencies) > 7 || !contains(s.Site.Currencies, s.Site.Currency) {
		return errors.New("请选择支持的币种与默认币种")
	}
	seen := map[string]bool{}
	for _, c := range s.Site.Currencies {
		if _, ok := supportedCurrencies[c]; !ok || seen[c] {
			return fmt.Errorf("无效或重复币种: %s", c)
		}
		seen[c] = true
	}
	if len(s.Site.Languages) == 0 || len(s.Site.Languages) > 3 || !contains(s.Site.Languages, s.Site.DefaultLanguage) {
		return errors.New("请选择语言与默认语言")
	}
	seen = map[string]bool{}
	for _, l := range s.Site.Languages {
		if !supportedLanguages[l] || seen[l] || !contains(s.Site.Currencies, s.Site.LanguageCurrencies[l]) {
			return errors.New("语言或其默认币种无效")
		}
		seen[l] = true
	}
	if len(s.Site.Presets) == 0 || len(s.Site.Presets) > 8 {
		return errors.New("预设金额需要 1 到 8 个")
	}
	for _, a := range s.Site.Presets {
		if a <= 0 || a > 1000000 {
			return errors.New("预设金额应在 1 到 1000000 之间")
		}
	}
	for l, t := range s.Site.Translations {
		if !supportedLanguages[l] || len(t.Tagline) > 500 || len(t.Description) > 10000 || len(t.Footer) > 2000 || len(t.Terms) > 20000 || len(t.Privacy) > 20000 {
			return errors.New("翻译文本无效或过长")
		}
	}
	if s.Site.ContactEmail != "" {
		if _, e := mail.ParseAddress(s.Site.ContactEmail); e != nil {
			return errors.New("联系邮箱无效")
		}
	}
	if len(s.Methods) > 20 {
		return errors.New("最多配置 20 个支付方式")
	}
	seen = map[string]bool{}
	for _, m := range s.Methods {
		if !validID(m.ID) || seen[m.ID] || strings.TrimSpace(m.Name) == "" || len(m.Name) > 120 || len(m.Description) > 3000 {
			return errors.New("支付方式标识、名称或说明无效")
		}
		seen[m.ID] = true
		if m.QRURL != "" && !strings.HasPrefix(m.QRURL, "/uploads/") {
			return errors.New("二维码请通过上传接口添加")
		}
		if m.CheckoutURL != "" {
			u, e := url.Parse(m.CheckoutURL)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
				return errors.New("自定义支付链接必须为 HTTPS")
			}
		}
		if e := payments.ValidateMethod(m); e != nil {
			return e
		}
	}
	if len(s.StatsToken) < 32 || len(s.StatsToken) > 256 {
		return errors.New("统计接口令牌至少需要 32 个字符")
	}
	return notify.Validate(notify.Config{Webhook: s.Webhook, SMTP: s.SMTP})
}
func (a *App) settings() (Settings, error) {
	var raw string
	err := a.DB.QueryRow("SELECT value FROM settings WHERE key='main'").Scan(&raw)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
func (a *App) saveSettings(s Settings) error {
	raw, e := json.Marshal(s)
	if e != nil {
		return e
	}
	_, e = a.DB.Exec("INSERT INTO settings(key,value) VALUES('main',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw))
	return e
}
