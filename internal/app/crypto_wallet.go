package app

import (
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/chain"
)

func (a *App) checkCryptoNetwork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Network chain.Network `json:"network"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "配置无效")
		return
	}
	cfg := chain.Defaults()
	cfg.Networks = []chain.Network{in.Network}
	cfg.Networks[0].Enabled = false
	if e := cfg.Validate(); e != nil {
		fail(w, 400, e.Error())
		return
	}
	e := a.checkNetwork(r, in.Network)
	if e != nil {
		respond(w, 200, map[string]any{"ok": false, "reason": e.Error()})
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) checkNetwork(r *http.Request, n chain.Network) error {
	switch n.Family {
	case "evm":
		if e := a.Chain.CheckEVM(r.Context(), n); e != nil {
			return e
		}
		for _, t := range n.Tokens {
			if !t.Enabled {
				continue
			}
			var code string
			if e := a.Chain.RPC(r.Context(), n, "eth_getCode", []any{t.Contract, "latest"}, &code); e != nil {
				return e
			}
			if code == "0x" || code == "" {
				return errors.New("wrong_token")
			}
			var dec string
			if e := a.Chain.RPC(r.Context(), n, "eth_call", []any{map[string]string{"to": t.Contract, "data": "0x313ce567"}, "latest"}, &dec); e != nil {
				return e
			}
			if len(dec) < 3 {
				return errors.New("wrong_decimals")
			}
			raw, e := hex.DecodeString(dec[2:])
			if e != nil || new(big.Int).SetBytes(raw).Cmp(big.NewInt(int64(t.Decimals))) != 0 {
				return errors.New("wrong_decimals")
			}
		}
	case "solana":
		if e := a.Chain.CheckGenesis(r.Context(), n); e != nil {
			return e
		}
		var health string
		if e := a.Chain.RPC(r.Context(), n, "getHealth", []any{}, &health); e != nil {
			return e
		}
		if health != "ok" {
			return errors.New("rpc_unavailable")
		}
		for _, t := range n.Tokens {
			if !t.Enabled {
				continue
			}
			var info struct {
				Value *struct {
					Owner string `json:"owner"`
					Data  struct {
						Parsed struct {
							Type string `json:"type"`
							Info struct {
								Decimals    int  `json:"decimals"`
								Initialized bool `json:"isInitialized"`
							} `json:"info"`
						} `json:"parsed"`
					} `json:"data"`
				} `json:"value"`
			}
			if e := a.Chain.RPC(r.Context(), n, "getAccountInfo", []any{t.Contract, map[string]string{"encoding": "jsonParsed", "commitment": "finalized"}}, &info); e != nil {
				return e
			}
			if info.Value == nil || info.Value.Owner != "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA" || info.Value.Data.Parsed.Type != "mint" || !info.Value.Data.Parsed.Info.Initialized {
				return errors.New("wrong_token")
			}
			if info.Value.Data.Parsed.Info.Decimals != t.Decimals {
				return errors.New("wrong_decimals")
			}
		}
	case "tron":
		if e := a.Chain.CheckGenesis(r.Context(), n); e != nil {
			return e
		}
		for _, t := range n.Tokens {
			if !t.Enabled {
				continue
			}
			var result struct {
				Result struct {
					OK bool `json:"result"`
				} `json:"result"`
				Values []string `json:"constant_result"`
			}
			if e := a.Chain.Request(r.Context(), n, "/wallet/triggerconstantcontract", map[string]any{"owner_address": t.Contract, "contract_address": t.Contract, "function_selector": "decimals()", "visible": true}, &result); e != nil {
				return e
			}
			if !result.Result.OK || len(result.Values) != 1 {
				return errors.New("wrong_token")
			}
			raw, e := hex.DecodeString(result.Values[0])
			if e != nil || new(big.Int).SetBytes(raw).Cmp(big.NewInt(int64(t.Decimals))) != 0 {
				return errors.New("wrong_decimals")
			}
		}
	}
	return nil
}
func (a *App) cryptoWalletData(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) || !a.rateLimit(r) {
		fail(w, 429, "请求过于频繁或来源无效")
		return
	}
	var in struct {
		Token   string `json:"status_token"`
		Account string `json:"account"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "请求无效")
		return
	}
	d, e := a.authorizeCrypto(r, in.Token)
	if e != nil {
		fail(w, 404, "订单无效")
		return
	}
	p, e := readCrypto(a.DB, d.ID)
	if e != nil {
		internalError(w)
		return
	}
	if p.Quote.Network.Family != "solana" || !chain.AddressValid("solana", in.Account) {
		fail(w, 400, "钱包地址无效")
		return
	}
	if d.Status == "confirmed" || d.Status == "refunded" || time.Now().Unix() > p.Deadline {
		fail(w, 409, "此订单已结束，请核对交易记录")
		return
	}
	var result struct {
		Value struct {
			Hash   string `json:"blockhash"`
			Height uint64 `json:"lastValidBlockHeight"`
		} `json:"value"`
	}
	if e = a.Chain.RPC(r.Context(), p.Quote.Network, "getLatestBlockhash", []any{map[string]string{"commitment": "confirmed"}}, &result); e != nil {
		respond(w, 503, map[string]string{"error": "节点暂不可用，请稍后重试", "reason": "rpc_unavailable"})
		return
	}
	respond(w, 200, map[string]any{"blockhash": result.Value.Hash, "last_valid_block_height": result.Value.Height})
}
