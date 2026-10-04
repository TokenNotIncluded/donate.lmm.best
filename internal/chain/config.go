// Package chain verifies public chain evidence. It never holds a signing key.
package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"

	"github.com/mr-tron/base58"
)

type Token struct {
	Asset    string `json:"asset"`
	Label    string `json:"label"`
	Contract string `json:"contract"`
	Decimals int    `json:"decimals"`
	Enabled  bool   `json:"enabled"`
}
type Network struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Family        string  `json:"family"`
	Enabled       bool    `json:"enabled"`
	Recipient     string  `json:"recipient"` // Empty inherits the family's address.
	ChainID       int64   `json:"chain_id"`
	GenesisHash   string  `json:"genesis_hash,omitempty"`
	RPCURL        string  `json:"rpc_url"`
	WSURL         string  `json:"ws_url"`
	APIKey        string  `json:"api_key"`
	Explorer      string  `json:"explorer"`
	Confirmations uint64  `json:"confirmations"`
	Finality      string  `json:"finality"`
	NativeSymbol  string  `json:"native_symbol"`
	WalletRPCURL  string  `json:"wallet_rpc_url,omitempty"`
	Tokens        []Token `json:"tokens"`
}
type Config struct {
	Addresses         map[string]string `json:"addresses"`
	Networks          []Network         `json:"networks"`
	ToleranceBPS      int64             `json:"tolerance_bps"`
	LifetimeMinutes   int               `json:"lifetime_minutes"`
	ConfirmationHours int               `json:"confirmation_hours"`
	AcceptLate        bool              `json:"accept_late"`
	EventSecret       string            `json:"event_secret"`
}
type Quote struct {
	Network           Network `json:"network"`
	Token             Token   `json:"token"`
	Recipient         string  `json:"recipient"`
	Expected          string  `json:"expected_units"`
	ToleranceBPS      int64   `json:"tolerance_bps"`
	AcceptLate        bool    `json:"accept_late"`
	ConfirmationHours int     `json:"confirmation_hours"`
}
type Option struct {
	Network string `json:"network"`
	Name    string `json:"name"`
	Family  string `json:"family"`
	Asset   string `json:"asset"`
	Label   string `json:"label"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var assetPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,11}$`)
var evmAddress = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
var evmHash = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
var tronHash = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func AddressValid(family, address string) bool {
	switch family {
	case "evm":
		return evmAddress.MatchString(address) && strings.Trim(strings.ToLower(address[2:]), "0") != ""
	case "solana":
		b, e := base58.Decode(address)
		return e == nil && len(b) == 32
	case "tron":
		b, e := base58.Decode(address)
		if e != nil || len(b) != 25 || b[0] != 0x41 {
			return false
		}
		h := sha256.Sum256(b[:21])
		h = sha256.Sum256(h[:])
		return string(b[21:]) == string(h[:4])
	}
	return false
}
func NormalizeTx(family, id string) (string, error) {
	id = strings.TrimSpace(id)
	switch family {
	case "evm":
		if evmHash.MatchString(id) {
			return strings.ToLower(id), nil
		}
	case "tron":
		if tronHash.MatchString(id) {
			return strings.ToLower(id), nil
		}
	case "solana":
		b, e := base58.Decode(id)
		if e == nil && len(b) == 64 {
			return id, nil
		}
	}
	return "", errors.New("invalid_tx_id")
}
func ValidateURL(raw string, ws bool) bool {
	u, e := url.Parse(raw)
	scheme := "https"
	if ws {
		scheme = "wss"
	}
	return e == nil && u.Scheme == scheme && u.Hostname() != "" && u.User == nil && u.Fragment == "" && len(raw) <= 2048 && !strings.ContainsAny(raw, "\r\n\\")
}
func (c Config) Validate() error {
	if len(c.Networks) > 32 || c.ToleranceBPS < 0 || c.ToleranceBPS > 1000 || c.LifetimeMinutes < 5 || c.LifetimeMinutes > 1440 || c.ConfirmationHours < 1 || c.ConfirmationHours > 168 {
		return errors.New("链上配置或容差无效（少付容差最多 10%）")
	}
	for f, a := range c.Addresses {
		if f != "evm" && f != "tron" && f != "solana" {
			return errors.New("地址类型无效")
		}
		if a != "" && !AddressValid(f, a) {
			return fmt.Errorf("%s 收款地址无效", f)
		}
	}
	if c.EventSecret != "" && (len(c.EventSecret) < 32 || len(c.EventSecret) > 256) {
		return errors.New("链事件签名密钥需为 32 到 256 字符")
	}
	ids := map[string]bool{}
	for _, n := range c.Networks {
		if !idPattern.MatchString(n.ID) || ids[n.ID] || n.Name == "" || len(n.Name) > 80 || !containsFamily(n.Family) || len(n.Tokens) > 12 {
			return errors.New("链名称、标识或类型无效")
		}
		ids[n.ID] = true
		if n.Recipient != "" && !AddressValid(n.Family, n.Recipient) {
			return fmt.Errorf("%s 收款地址无效", n.Name)
		}
		explorer, _ := url.Parse(n.Explorer)
		validExplorer := explorer != nil && explorer.Scheme == "https" && explorer.Hostname() != "" && explorer.User == nil && len(n.Explorer) <= 2048
		if !ValidateURL(n.RPCURL, false) || n.WSURL != "" && !ValidateURL(n.WSURL, true) || n.WalletRPCURL != "" && !ValidateURL(n.WalletRPCURL, false) || !validExplorer || strings.ContainsAny(n.APIKey, "\r\n") || len(n.APIKey) > 2048 {
			return fmt.Errorf("%s 节点或浏览器网址无效", n.Name)
		}
		if n.Enabled && !AddressValid(n.Family, c.Recipient(n)) {
			return fmt.Errorf("请先配置 %s 收款地址", n.Name)
		}
		if n.Family == "evm" && (n.ChainID < 1 || n.ChainID > 1<<53 || n.Confirmations < 1 || n.Confirmations > 10000 || n.Finality != "confirmations" && n.Finality != "finalized") {
			return errors.New("EVM 链 ID 或确认策略无效")
		}
		if n.Family == "solana" && !AddressValid("solana", n.GenesisHash) || n.Family == "tron" && !tronHash.MatchString(n.GenesisHash) {
			return errors.New("请配置主网创世区块标识")
		}
		seen := map[string]bool{}
		for _, t := range n.Tokens {
			if !assetPattern.MatchString(t.Asset) || seen[t.Asset] || !AddressValid(n.Family, t.Contract) || t.Decimals < 2 || t.Decimals > 18 || len(t.Label) > 50 {
				return fmt.Errorf("%s 币种合约或精度无效", n.Name)
			}
			seen[t.Asset] = true
		}
	}
	return nil
}
func containsFamily(f string) bool { return f == "evm" || f == "tron" || f == "solana" }

// Replay protection identifies the chain itself, regardless of an operator's
// display ID, aliases, token selection or recipient-address changes.
func (n Network) ReplayKey() string {
	if n.Family == "evm" {
		return fmt.Sprintf("evm:%d", n.ChainID)
	}
	return n.Family + ":" + n.GenesisHash
}
func (c Config) Recipient(n Network) string {
	if n.Recipient != "" {
		return n.Recipient
	}
	return c.Addresses[n.Family]
}
func (c Config) Options() []Option {
	out := []Option{}
	for _, n := range c.Networks {
		if !n.Enabled || !AddressValid(n.Family, c.Recipient(n)) {
			continue
		}
		for _, t := range n.Tokens {
			if t.Enabled {
				out = append(out, Option{n.ID, n.Name, n.Family, t.Asset, t.Label})
			}
		}
	}
	return out
}
func (c Config) Quote(network, asset string, minor int64) (Quote, error) {
	for _, n := range c.Networks {
		if n.ID != network || !n.Enabled {
			continue
		}
		for _, t := range n.Tokens {
			if t.Asset == asset && t.Enabled {
				if !AddressValid(n.Family, c.Recipient(n)) || minor <= 0 {
					break
				}
				units := new(big.Int).Mul(big.NewInt(minor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(t.Decimals-2)), nil))
				return Quote{n, t, c.Recipient(n), units.String(), c.ToleranceBPS, c.AcceptLate, c.ConfirmationHours}, nil
			}
		}
	}
	return Quote{}, errors.New("请选择已启用的币种和网络")
}
func Units(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 || len(s) > 78 {
		return nil, errors.New("invalid_amount")
	}
	return v, nil
}
func Decimal(units string, decimals int) string {
	s := strings.Repeat("0", max(0, decimals+1-len(units))) + units
	if decimals == 0 {
		return s
	}
	s = s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

// Minimum is ceil(expected * (1 - tolerance)). A nonzero payment is mandatory.
func Minimum(expected string, bps int64) *big.Int {
	v, _ := Units(expected)
	v.Mul(v, big.NewInt(10000-bps))
	v.Add(v, big.NewInt(9999))
	return v.Div(v, big.NewInt(10000))
}
func PaymentURI(q Quote, units string) string {
	switch q.Network.Family {
	case "evm":
		return fmt.Sprintf("ethereum:%s@%d/transfer?address=%s&uint256=%s", q.Token.Contract, q.Network.ChainID, q.Recipient, units)
	case "solana":
		return "solana:" + q.Recipient + "?" + url.Values{"amount": {Decimal(units, q.Token.Decimals)}, "spl-token": {q.Token.Contract}, "label": {"Donate"}}.Encode()
	default:
		return q.Recipient
	}
}
func TronHex(s string) string {
	b, e := base58.Decode(s)
	if e != nil || len(b) != 25 {
		return ""
	}
	return hex.EncodeToString(b[1:21])
}
