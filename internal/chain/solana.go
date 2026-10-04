package chain

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
)

type solBalance struct {
	Index  int    `json:"accountIndex"`
	Mint   string `json:"mint"`
	Owner  string `json:"owner"`
	Amount struct {
		Units    string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}
type solTransaction struct {
	Slot        uint64 `json:"slot"`
	Time        int64  `json:"blockTime"`
	Transaction struct {
		Signatures []string `json:"signatures"`
	} `json:"transaction"`
	Meta *struct {
		Err  json.RawMessage `json:"err"`
		Pre  []solBalance    `json:"preTokenBalances"`
		Post []solBalance    `json:"postTokenBalances"`
	} `json:"meta"`
}

func (s *Service) solana(ctx context.Context, q Quote, id string) (Evidence, error) {
	var statuses struct {
		Value []*struct {
			Err          json.RawMessage `json:"err"`
			Confirmation string          `json:"confirmationStatus"`
		} `json:"value"`
	}
	if e := s.RPC(ctx, q.Network, "getSignatureStatuses", []any{[]string{id}, map[string]any{"searchTransactionHistory": true}}, &statuses); e != nil {
		return Evidence{}, e
	}
	if len(statuses.Value) != 1 || statuses.Value[0] == nil {
		return waiting(), nil
	}
	st := statuses.Value[0]
	if len(st.Err) > 0 && string(st.Err) != "null" {
		v := invalid("execution_failed")
		v.State = "failed"
		return v, nil
	}
	final := st.Confirmation == "finalized"
	commitment := "confirmed"
	if final {
		commitment = "finalized"
	}
	var r *solTransaction
	if e := s.RPC(ctx, q.Network, "getTransaction", []any{id, map[string]any{"encoding": "jsonParsed", "commitment": commitment, "maxSupportedTransactionVersion": 0}}, &r); e != nil {
		return Evidence{}, e
	}
	if r == nil {
		return waiting(), nil
	}
	if r.Meta == nil || len(r.Transaction.Signatures) == 0 || r.Transaction.Signatures[0] != id {
		return Evidence{}, errors.New("rpc_unavailable")
	}
	if len(r.Meta.Err) == 0 || string(r.Meta.Err) != "null" {
		v := invalid("execution_failed")
		v.State = "failed"
		return v, nil
	}
	// Net balance change of token accounts owned by the merchant is the amount
	// actually received. Parsed instructions alone can overcount round trips.
	sum := new(big.Int)
	found := false
	for _, b := range r.Meta.Post {
		if b.Mint != q.Token.Contract || b.Owner != q.Recipient {
			continue
		}
		found = true
		if b.Amount.Decimals != q.Token.Decimals {
			return Evidence{}, errors.New("wrong_decimals")
		}
		v, e := Units(b.Amount.Units)
		if e != nil {
			return Evidence{}, e
		}
		sum.Add(sum, v)
	}
	for _, b := range r.Meta.Pre {
		if b.Mint != q.Token.Contract || b.Owner != q.Recipient {
			continue
		}
		if b.Amount.Decimals != q.Token.Decimals {
			return Evidence{}, errors.New("wrong_decimals")
		}
		v, e := Units(b.Amount.Units)
		if e != nil {
			return Evidence{}, e
		}
		sum.Sub(sum, v)
	}
	if sum.Sign() <= 0 {
		if found {
			return invalid("no_transfer"), nil
		}
		return invalid("wrong_token_or_recipient"), nil
	}
	state := "confirming"
	if final {
		state = "valid"
	}
	return Evidence{State: state, Amount: sum.String(), Block: r.Slot, Time: r.Time, Final: final}, nil
}
