package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type Evidence struct {
	State         string `json:"state"`
	Reason        string `json:"reason"`
	Amount        string `json:"amount_units"`
	Block         uint64 `json:"block"`
	BlockHash     string `json:"block_hash"`
	Time          int64  `json:"block_time"`
	Confirmations uint64 `json:"confirmations"`
	Final         bool   `json:"final"`
}
type Service struct{ Client *http.Client }

func (s *Service) Request(ctx context.Context, n Network, path string, in, out any) error {
	return s.request(ctx, n, path, in, out)
}
func New() *Service { return &Service{Client: PublicClient()} }
func PublicClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, errors.New("rpc_unavailable")
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("rpc_unavailable")
			}
		}
		for _, ip := range ips {
			c, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return c, nil
			}
		}
		return nil, errors.New("rpc_unavailable")
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return !ip.Is6() || netip.MustParsePrefix("2000::/3").Contains(ip)
}
func (s *Service) request(ctx context.Context, n Network, path string, in, out any) error {
	raw, e := json.Marshal(in)
	if e != nil {
		return errors.New("rpc_unavailable")
	}
	endpoint := n.RPCURL
	if path != "" {
		endpoint = strings.TrimRight(endpoint, "/") + path
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return errors.New("rpc_unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	if n.APIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", n.APIKey)
	}
	resp, e := s.Client.Do(req)
	if e != nil {
		return errors.New("rpc_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("rpc_unavailable")
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil || len(data) > 4<<20 || json.Unmarshal(data, out) != nil {
		return errors.New("rpc_unavailable")
	}
	return nil
}
func (s *Service) RPC(ctx context.Context, n Network, method string, params any, out any) error {
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if e := s.request(ctx, n, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, &response); e != nil {
		return e
	}
	if len(response.Error) > 0 && string(response.Error) != "null" || len(response.Result) == 0 {
		return errors.New("rpc_unavailable")
	}
	if json.Unmarshal(response.Result, out) != nil {
		return errors.New("rpc_unavailable")
	}
	return nil
}
func (s *Service) Verify(ctx context.Context, q Quote, txID string) (Evidence, error) {
	id, e := NormalizeTx(q.Network.Family, txID)
	if e != nil {
		return Evidence{}, e
	}
	switch q.Network.Family {
	case "evm":
		return s.evm(ctx, q, id)
	case "tron":
		if e := s.CheckGenesis(ctx, q.Network); e != nil {
			return Evidence{}, e
		}
		return s.tron(ctx, q, id)
	case "solana":
		if e := s.CheckGenesis(ctx, q.Network); e != nil {
			return Evidence{}, e
		}
		return s.solana(ctx, q, id)
	}
	return Evidence{}, errors.New("unsupported_network")
}
func (s *Service) CheckGenesis(ctx context.Context, n Network) error {
	var actual string
	if n.Family == "solana" {
		if e := s.RPC(ctx, n, "getGenesisHash", []any{}, &actual); e != nil {
			return e
		}
	} else {
		var block struct {
			ID string `json:"blockID"`
		}
		if e := s.request(ctx, n, "/wallet/getblockbynum", map[string]int{"num": 0}, &block); e != nil {
			return e
		}
		actual = block.ID
	}
	if actual == "" || actual != n.GenesisHash {
		return errors.New("wrong_network")
	}
	return nil
}
func waiting() Evidence              { return Evidence{State: "submitted", Reason: "not_found", Amount: "0"} }
func invalid(reason string) Evidence { return Evidence{State: "invalid", Reason: reason, Amount: "0"} }
