package chain

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
)

const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type evmLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
	Removed bool     `json:"removed"`
}
type evmReceipt struct {
	Hash      string   `json:"transactionHash"`
	Status    string   `json:"status"`
	Block     string   `json:"blockNumber"`
	BlockHash string   `json:"blockHash"`
	Logs      []evmLog `json:"logs"`
}
type evmBlock struct {
	Number string `json:"number"`
	Hash   string `json:"hash"`
	Time   string `json:"timestamp"`
}

func hexUint(s string) (uint64, error) {
	if !strings.HasPrefix(s, "0x") {
		return 0, errors.New("rpc_unavailable")
	}
	v, e := strconv.ParseUint(s[2:], 16, 64)
	if e != nil {
		return 0, errors.New("rpc_unavailable")
	}
	return v, nil
}
func (s *Service) CheckEVM(ctx context.Context, n Network) error {
	var id string
	if e := s.RPC(ctx, n, "eth_chainId", []any{}, &id); e != nil {
		return e
	}
	v, e := hexUint(id)
	if e != nil || v != uint64(n.ChainID) {
		return errors.New("wrong_network")
	}
	return nil
}
func (s *Service) evm(ctx context.Context, q Quote, id string) (Evidence, error) {
	n := q.Network
	if e := s.CheckEVM(ctx, n); e != nil {
		return Evidence{}, e
	}
	var r *evmReceipt
	if e := s.RPC(ctx, n, "eth_getTransactionReceipt", []any{id}, &r); e != nil {
		return Evidence{}, e
	}
	if r == nil {
		return waiting(), nil
	}
	if !strings.EqualFold(r.Hash, id) {
		return Evidence{}, errors.New("rpc_unavailable")
	}
	if r.Status == "0x0" {
		v := invalid("execution_failed")
		v.State = "failed"
		return v, nil
	}
	if r.Status != "0x1" {
		return Evidence{}, errors.New("rpc_unavailable")
	}
	amount := new(big.Int)
	tokenFound := false
	recipientTopic := "0x" + strings.Repeat("0", 24) + strings.ToLower(q.Recipient[2:])
	for _, l := range r.Logs {
		if l.Removed || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], transferTopic) {
			continue
		}
		if !strings.EqualFold(l.Address, q.Token.Contract) {
			continue
		}
		tokenFound = true
		incoming := strings.EqualFold(l.Topics[2], recipientTopic)
		outgoing := strings.EqualFold(l.Topics[1], recipientTopic)
		if !incoming && !outgoing {
			continue
		}
		if len(l.Data) != 66 || !strings.HasPrefix(l.Data, "0x") {
			return Evidence{}, errors.New("rpc_unavailable")
		}
		v, ok := new(big.Int).SetString(l.Data[2:], 16)
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
		if tokenFound {
			return invalid("wrong_recipient"), nil
		}
		return invalid("wrong_token"), nil
	}
	block, e := hexUint(r.Block)
	if e != nil {
		return Evidence{}, e
	}
	var canonical *evmBlock
	if e = s.RPC(ctx, n, "eth_getBlockByNumber", []any{r.Block, false}, &canonical); e != nil {
		return Evidence{}, e
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, r.BlockHash) {
		return waiting(), nil
	}
	timestamp, e := hexUint(canonical.Time)
	if e != nil {
		return Evidence{}, e
	}
	var latest string
	if e = s.RPC(ctx, n, "eth_blockNumber", []any{}, &latest); e != nil {
		return Evidence{}, e
	}
	head, e := hexUint(latest)
	if e != nil || head < block {
		return waiting(), nil
	}
	v := Evidence{State: "confirming", Amount: amount.String(), Block: block, BlockHash: r.BlockHash, Time: int64(timestamp), Confirmations: head - block + 1}
	v.Final = v.Confirmations >= n.Confirmations
	if n.Finality == "finalized" {
		var finalized *evmBlock
		if e = s.RPC(ctx, n, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); e != nil {
			return Evidence{}, e
		}
		if finalized == nil {
			v.Final = false
		} else {
			height, e := hexUint(finalized.Number)
			if e != nil {
				return Evidence{}, e
			}
			v.Final = v.Final && height >= block
		}
	}
	if v.Final {
		v.State = "valid"
	}
	return v, nil
}
