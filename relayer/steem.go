package relayer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Transfer is one Steem transfer operation addressed to the gateway account,
// carrying exactly the raw facts a validator attests on-chain. All fields
// must be derived deterministically from Steem block data so every honest
// validator submits identical values (the module rejects mismatches).
type Transfer struct {
	Txid             string
	OpIndex          uint32
	SteemBlock       uint64
	SteemTimestamp   string
	From             string
	AmountMillisteem uint64
	Memo             string
}

// SteemClient is a minimal Steem condenser_api JSON-RPC client. The relayer
// only reads (dynamic global properties + blocks), so a full SDK dependency
// is deliberately avoided: fewer supply-chain dependencies in a validator
// binary, and Steem's own timestamp strings are used verbatim.
type SteemClient struct {
	url  string
	http *http.Client
}

func NewSteemClient(rpcURL string) *SteemClient {
	return &SteemClient{
		url:  rpcURL,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      uint64 `json:"id"`
}

type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *SteemClient) call(method string, params []any, result any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return err
	}
	resp, err := c.http.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return fmt.Errorf("steem rpc: invalid response: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("steem rpc: %s (code %d)", rpcResp.Error.Message, rpcResp.Error.Code)
	}
	return json.Unmarshal(rpcResp.Result, result)
}

// steemBlock mirrors the condenser_api.get_block result — only the fields
// the relayer needs.
type steemBlock struct {
	Timestamp      string    `json:"timestamp"`
	TransactionIds []string  `json:"transaction_ids"`
	Transactions   []steemTx `json:"transactions"`
}

type steemTx struct {
	TransactionId string            `json:"transaction_id"`
	Operations    []json.RawMessage `json:"operations"`
}

// transferOp is the payload of a ["transfer", {...}] operation tuple.
type transferOp struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
}

// NumberedBlock pairs a Steem block with its height (the block payload does
// not carry its own number).
type NumberedBlock struct {
	Num   uint64
	Block *steemBlock
}

// LastIrreversibleBlock returns Steem's current last irreversible block —
// the relayer only ever scans irreversible blocks, so attested facts can
// never be undone by a Steem fork.
func (c *SteemClient) LastIrreversibleBlock() (uint64, error) {
	var dgp struct {
		LastIrreversibleBlockNum uint64 `json:"last_irreversible_block_num"`
	}
	if err := c.call("condenser_api.get_dynamic_global_properties", []any{}, &dgp); err != nil {
		return 0, err
	}
	if dgp.LastIrreversibleBlockNum == 0 {
		return 0, fmt.Errorf("steem rpc: zero last irreversible block")
	}
	return dgp.LastIrreversibleBlockNum, nil
}

// FetchBlocks returns blocks from..to inclusive, fetched sequentially. A
// null result for a block at or below the last irreversible block is an
// error, not a skip: it means the RPC endpoint (e.g. a lagging backend in a
// load-balanced cluster) has not served the block — aborting the cycle makes
// the relayer retry rather than silently stepping the cursor over it.
func (c *SteemClient) FetchBlocks(from, to uint64) ([]NumberedBlock, error) {
	if from > to {
		return nil, nil
	}
	blocks := make([]NumberedBlock, 0, to-from+1)
	for num := from; num <= to; num++ {
		var block *steemBlock
		if err := c.call("condenser_api.get_block", []any{num}, &block); err != nil {
			return nil, fmt.Errorf("fetching steem block %d: %w", num, err)
		}
		if block == nil {
			return nil, fmt.Errorf("steem rpc returned no data for irreversible block %d", num)
		}
		blocks = append(blocks, NumberedBlock{Num: num, Block: block})
	}
	return blocks, nil
}

// ExtractGatewayTransfers scans a block for STEEM transfer operations whose
// recipient is the gateway account. Non-transfer operations, transfers to
// other accounts, and non-STEEM amounts (SBD) are skipped. OpIndex is the
// operation's index within its transaction, matching the module's
// (txid, op_index) dedup key. The block's timestamp string is used verbatim
// so all validators submit byte-identical facts.
func ExtractGatewayTransfers(blockNum uint64, block *steemBlock, gateway string) []Transfer {
	if block == nil {
		return nil
	}

	var transfers []Transfer
	for txNum, tx := range block.Transactions {
		txid := tx.TransactionId
		if txid == "" && txNum < len(block.TransactionIds) {
			txid = block.TransactionIds[txNum]
		}
		if txid == "" {
			continue
		}

		for opIndex, rawOp := range tx.Operations {
			// Operations are ["type", {body}] tuples.
			var tuple []json.RawMessage
			if err := json.Unmarshal(rawOp, &tuple); err != nil || len(tuple) != 2 {
				continue
			}
			var opType string
			if err := json.Unmarshal(tuple[0], &opType); err != nil || opType != "transfer" {
				continue
			}
			var op transferOp
			if err := json.Unmarshal(tuple[1], &op); err != nil {
				continue
			}
			if op.To != gateway {
				continue
			}
			amount, ok := ParseSteemAmount(op.Amount)
			if !ok {
				continue
			}
			transfers = append(transfers, Transfer{
				Txid:             txid,
				OpIndex:          uint32(opIndex), //nolint:gosec // ops per tx are tiny
				SteemBlock:       blockNum,
				SteemTimestamp:   block.Timestamp,
				From:             op.From,
				AmountMillisteem: amount,
				Memo:             op.Memo,
			})
		}
	}
	return transfers
}

// ParseSteemAmount converts a Steem asset string like "70.561 STEEM" into
// millisteem (70561). Only the STEEM symbol qualifies; SBD/VESTS and
// malformed amounts return ok=false. Parsing is integer-only — floats would
// risk validator-to-validator divergence.
func ParseSteemAmount(amount string) (millisteem uint64, ok bool) {
	parts := strings.Split(strings.TrimSpace(amount), " ")
	if len(parts) != 2 || parts[1] != "STEEM" {
		return 0, false
	}

	value := parts[0]
	intPart := value
	fracPart := ""
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		intPart, fracPart = value[:dot], value[dot+1:]
	}
	// STEEM has exactly 3 decimal places; accept fewer, reject more.
	if intPart == "" || len(fracPart) > 3 {
		return 0, false
	}
	fracPart += strings.Repeat("0", 3-len(fracPart))

	var result uint64
	for _, digits := range []string{intPart, fracPart} {
		for _, ch := range digits {
			if ch < '0' || ch > '9' {
				return 0, false
			}
			if result > (1<<63)/10 {
				return 0, false // overflow guard; no real balance gets close
			}
			result = result*10 + uint64(ch-'0')
		}
	}
	return result, true
}
