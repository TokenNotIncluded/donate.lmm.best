package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/mr-tron/base58"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockService(t *testing.T, reply func(string, []json.RawMessage, string) any) *Service {
	t.Helper()
	return &Service{Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &body); e != nil {
			return nil, e
		}
		v := reply(body.Method, body.Params, r.URL.Path)
		if body.Method != "" {
			v = map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}
		}
		data, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}
}
func fixtureQuote(family string) Quote {
	cfg := Defaults()
	for _, n := range cfg.Networks {
		if n.Family == family {
			recipient := "0x" + strings.Repeat("1", 40)
			if family == "tron" {
				recipient = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
			}
			if family == "solana" {
				recipient = base58.Encode(bytes.Repeat([]byte{3}, 32))
			}
			n.Confirmations = 3
			return Quote{Network: n, Token: n.Tokens[0], Recipient: recipient, Expected: "10000000", ToleranceBPS: 100, AcceptLate: true, ConfirmationHours: 24}
		}
	}
	panic(family)
}
func TestAmountsAndConfig(t *testing.T) {
	c := Defaults()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(c.Options()) != 0 {
		t.Fatal("defaults must not enable an address")
	}
	for _, a := range c.Addresses {
		if a != "" {
			t.Fatal("defaults contain recipient")
		}
	}
	c.Addresses = map[string]string{"evm": "0x" + strings.Repeat("1", 40)}
	c.Networks = c.Networks[1:2]
	c.Networks[0].Enabled = true
	q, e := c.Quote("bsc", "USDT", 1234)
	if e != nil {
		t.Fatal(e)
	}
	if q.Expected != "12340000000000000000" || Decimal(q.Expected, 18) != "12.34" {
		t.Fatalf("18 decimal precision lost: %+v", q)
	}
	if Minimum("10000000", 100).String() != "9900000" || Minimum("101", 100).String() != "100" {
		t.Fatal("tolerance must round upward")
	}
	if Decimal("1", 6) != "0.000001" || Decimal("0", 6) != "0" {
		t.Fatal("decimal conversion")
	}
	if !strings.Contains(PaymentURI(q, q.Expected), "uint256=12340000000000000000") {
		t.Fatal("URI loses exact integer amount")
	}
	alias := q.Network
	alias.ID = "renamed"
	if alias.ReplayKey() != q.Network.ReplayKey() {
		t.Fatal("replay key depends on display ID")
	}
	c.ToleranceBPS = 1001
	if c.Validate() == nil {
		t.Fatal("excess tolerance accepted")
	}
	for _, address := range []string{"", "0x" + strings.Repeat("0", 40), "0x1234"} {
		if AddressValid("evm", address) {
			t.Fatal("bad address accepted")
		}
	}
	if !AddressValid("tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t") || AddressValid("tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u") {
		t.Fatal("base58 checksum not checked")
	}
	if _, e = NormalizeTx("evm", "https://localhost/tx"); e == nil {
		t.Fatal("URL accepted as transaction")
	}
}
func TestEVMEvidence(t *testing.T) {
	id := "0x" + strings.Repeat("a", 64)
	q := fixtureQuote("evm")
	to := "0x" + strings.Repeat("0", 24) + q.Recipient[2:]
	cases := []struct {
		name, state, reason        string
		alter                      func(*evmReceipt)
		wrongChain, reorg, unfinal bool
	}{
		{name: "final receipt", state: "valid"},
		{name: "confirming only", state: "confirming", unfinal: true},
		{name: "wrong chain", reason: "wrong_network", wrongChain: true},
		{name: "reorg", state: "submitted", reason: "not_found", reorg: true},
		{name: "wrong token", state: "invalid", reason: "wrong_token", alter: func(r *evmReceipt) { r.Logs[0].Address = "0x" + strings.Repeat("2", 40) }},
		{name: "wrong recipient", state: "invalid", reason: "wrong_recipient", alter: func(r *evmReceipt) { r.Logs[0].Topics[2] = "0x" + strings.Repeat("0", 64) }},
		{name: "self transfer cannot settle", state: "invalid", reason: "wrong_recipient", alter: func(r *evmReceipt) { r.Logs[0].Topics[1] = to }},
		{name: "execution reverted", state: "failed", reason: "execution_failed", alter: func(r *evmReceipt) { r.Status = "0x0" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := evmReceipt{Hash: id, Status: "0x1", Block: "0x64", BlockHash: "canonical", Logs: []evmLog{{Address: q.Token.Contract, Topics: []string{transferTopic, "0x" + strings.Repeat("0", 64), to}, Data: "0x" + fmt.Sprintf("%064x", big.NewInt(10000000))}}}
			if tc.alter != nil {
				tc.alter(&r)
			}
			s := mockService(t, func(m string, p []json.RawMessage, _ string) any {
				switch m {
				case "eth_chainId":
					if tc.wrongChain {
						return "0x1"
					}
					return fmt.Sprintf("0x%x", q.Network.ChainID)
				case "eth_getTransactionReceipt":
					return r
				case "eth_blockNumber":
					return "0x66"
				case "eth_getBlockByNumber":
					var tag string
					json.Unmarshal(p[0], &tag)
					if tag == "finalized" {
						if tc.unfinal {
							return evmBlock{Number: "0x63"}
						}
						return evmBlock{Number: "0x64"}
					}
					hash := "canonical"
					if tc.reorg {
						hash = "different"
					}
					return evmBlock{Number: "0x64", Hash: hash, Time: "0x65000000"}
				}
				t.Fatalf("unexpected RPC %s", m)
				return nil
			})
			v, e := s.Verify(context.Background(), q, id)
			if tc.wrongChain {
				if e == nil || e.Error() != tc.reason {
					t.Fatalf("wrong network result %+v %v", v, e)
				}
				return
			}
			if e != nil || v.State != tc.state || v.Reason != tc.reason {
				t.Fatalf("evidence %+v, error %v", v, e)
			}
			if v.State == "valid" && (v.Amount != "10000000" || !v.Final || v.Confirmations != 3) {
				t.Fatal(v)
			}
		})
	}
}
func TestTronUsesIrreversibleReceiptAndNetAmount(t *testing.T) {
	q := fixtureQuote("tron")
	id := strings.Repeat("b", 64)
	to := strings.Repeat("0", 24) + TronHex(q.Recipient)
	for _, final := range []bool{true, false} {
		t.Run(fmt.Sprint(final), func(t *testing.T) {
			var r tronReceipt
			r.ID = id
			r.Block = 10
			r.Time = 1700000000000
			r.Receipt.Result = "SUCCESS"
			raw := fmt.Sprintf(`[{"address":%q,"topics":[%q,%q,%q],"data":%q}]`, TronHex(q.Token.Contract), transferTopic[2:], strings.Repeat("0", 63)+"1", to, fmt.Sprintf("%064x", 10000000))
			json.Unmarshal([]byte(raw), &r.Logs)
			s := mockService(t, func(_ string, _ []json.RawMessage, path string) any {
				switch path {
				case "/wallet/getblockbynum":
					return map[string]string{"blockID": q.Network.GenesisHash}
				case "/walletsolidity/gettransactioninfobyid":
					if !final {
						return map[string]any{}
					}
					return r
				case "/wallet/gettransactioninfobyid":
					return r
				}
				t.Fatal(path)
				return nil
			})
			v, e := s.Verify(context.Background(), q, id)
			if e != nil || v.Final != final || v.Amount != "10000000" || v.Time != 1700000000 {
				t.Fatalf("%+v %v", v, e)
			}
			r.Logs[0].Topics[1] = to
			v, e = s.Verify(context.Background(), q, id)
			if e != nil || v.State != "invalid" {
				t.Fatalf("self transfer accepted %+v %v", v, e)
			}
		})
	}
}
func TestSolanaChecksFinalityAndNetBalances(t *testing.T) {
	q := fixtureQuote("solana")
	id := base58.Encode(bytes.Repeat([]byte{7}, 64))
	for _, test := range []struct {
		status, post, genesis, state, reason string
		decimals                             int
	}{{"confirmed", "12000000", q.Network.GenesisHash, "confirming", "", 6}, {"finalized", "12000000", q.Network.GenesisHash, "valid", "", 6}, {"finalized", "2000000", q.Network.GenesisHash, "invalid", "no_transfer", 6}, {"finalized", "12000000", q.Network.GenesisHash, "", "wrong_decimals", 9}, {"finalized", "12000000", base58.Encode(bytes.Repeat([]byte{1}, 32)), "", "wrong_network", 6}} {
		t.Run(test.status+test.post+test.reason, func(t *testing.T) {
			s := mockService(t, func(m string, _ []json.RawMessage, _ string) any {
				switch m {
				case "getGenesisHash":
					return test.genesis
				case "getSignatureStatuses":
					return map[string]any{"value": []any{map[string]any{"err": nil, "confirmationStatus": test.status}}}
				case "getTransaction":
					balance := func(amount string) any {
						return map[string]any{"accountIndex": 1, "mint": q.Token.Contract, "owner": q.Recipient, "uiTokenAmount": map[string]any{"amount": amount, "decimals": test.decimals}}
					}
					return map[string]any{"slot": 100, "blockTime": 1700000000, "transaction": map[string]any{"signatures": []string{id}}, "meta": map[string]any{"err": nil, "preTokenBalances": []any{balance("2000000")}, "postTokenBalances": []any{balance(test.post)}}}
				}
				t.Fatal(m)
				return nil
			})
			v, e := s.Verify(context.Background(), q, id)
			if test.state == "" {
				if e == nil || e.Error() != test.reason {
					t.Fatalf("%+v %v", v, e)
				}
				return
			}
			if e != nil || v.State != test.state || v.Reason != test.reason {
				t.Fatalf("%+v %v", v, e)
			}
			if v.State == "valid" && v.Amount != "10000000" {
				t.Fatal("did not subtract existing balance", v)
			}
		})
	}
}
func TestRPCTransportRejectsInternalNetworks(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "100.100.100.200", "169.254.169.254", "::1", "::ffff:192.168.1.1", "fc00::1", "2001:db8::1"} {
		if publicIP(netip.MustParseAddr(s)) {
			t.Fatal("private/reserved allowed", s)
		}
	}
	if !publicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address blocked")
	}
	client := PublicClient()
	_, e := client.Post("http://127.0.0.1:1", "application/json", strings.NewReader("{}"))
	if e == nil {
		t.Fatal("internal request allowed")
	}
}
