package chain

import (
	"context"
	"errors"
	"math/big"
	"strings"
)

type tronReceipt struct {
	ID      string `json:"id"`
	Block   uint64 `json:"blockNumber"`
	Time    int64  `json:"blockTimeStamp"`
	Result  string `json:"result"`
	Receipt struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Logs []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"log"`
}

func (s *Service) tron(ctx context.Context, q Quote, id string) (Evidence, error) {
	// SolidityNode is TRON's irreversible state, not the indexer's confirmed flag.
	var r tronReceipt
	final := true
	if e := s.request(ctx, q.Network, "/walletsolidity/gettransactioninfobyid", map[string]string{"value": id}, &r); e != nil {
		return Evidence{}, e
	}
	if r.ID == "" {
		final = false
		if e := s.request(ctx, q.Network, "/wallet/gettransactioninfobyid", map[string]string{"value": id}, &r); e != nil {
			return Evidence{}, e
		}
	}
	if r.ID == "" {
		return waiting(), nil
	}
	if !strings.EqualFold(r.ID, id) {
		return Evidence{}, errors.New("rpc_unavailable")
	}
	if r.Result == "FAILED" || r.Receipt.Result != "SUCCESS" {
		v := invalid("execution_failed")
		v.State = "failed"
		return v, nil
	}
	amount := new(big.Int)
	found := false
	to := strings.Repeat("0", 24) + TronHex(q.Recipient)
	for _, l := range r.Logs {
		address := l.Address
		if len(address) == 42 {
			address = strings.TrimPrefix(address, "41")
		}
		if len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], transferTopic[2:]) || !strings.EqualFold(address, TronHex(q.Token.Contract)) {
			continue
		}
		found = true
		incoming := strings.EqualFold(l.Topics[2], to)
		outgoing := strings.EqualFold(l.Topics[1], to)
		if !incoming && !outgoing {
			continue
		}
		if len(l.Data) != 64 {
			return Evidence{}, errors.New("rpc_unavailable")
		}
		v, ok := new(big.Int).SetString(l.Data, 16)
		if !ok {
			return Evidence{}, errors.New("rpc_unavailable")
		}
		if incoming {
			amount.Add(amount, v)
		}
		if outgoing {
			amount.Sub(amount, v)
		}
	}
	if amount.Sign() <= 0 {
		if found {
			return invalid("wrong_recipient"), nil
		}
		return invalid("wrong_token"), nil
	}
	state := "confirming"
	if final {
		state = "valid"
	}
	return Evidence{State: state, Amount: amount.String(), Block: r.Block, Time: r.Time / 1000, Final: final}, nil
}
