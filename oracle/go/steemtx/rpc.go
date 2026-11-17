// Package steemtx is the Steem transaction core used by the svm.bank multisig
// signer: JSON-RPC with node failover, the chain's binary serialization,
// transaction digest and ID, WIF/STM key handling, and canonical recoverable
// secp256k1 signatures. It follows oracle/multisigguide.md section 4 and is
// pinned byte-for-byte by oracle/multisig/vectors.json.
package steemtx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"steemvm/oracle/go/nodepool"
)

// RPC calls Steem nodes, own node first, moving to the next one on transport
// errors, HTML/non-JSON replies, node-side errors and answers that fail the
// caller's check. A node that fails is benched with a growing cooldown
// (oracle/go/nodepool), so a rate-limiting node is left alone instead of being
// hammered. Nodes are tried in a fixed order (Sticky), not round-robin, so
// consecutive reads of svm.bank's history come from one view of the chain. A
// chain-level rejection is returned immediately (another node would reject it
// too).
type RPC struct {
	Nodes []string
	HTTP  *http.Client
	pool  *nodepool.Pool
}

// ChainError is a rejection from the Steem chain itself (not a transport
// problem); it is never retried on another node.
type ChainError struct{ Message string }

func (e *ChainError) Error() string { return e.Message }

// Node-side errors worth trying the next node for. "Bad Cast" and "Internal
// Error" come from a misbehaving backend behind a load balancer
// (api.steemit.com) or a node without the API plugin a call needs
// (api.justyy.com has no account history), not from Steem itself; the same
// call succeeds on another node.
var transient = regexp.MustCompile(`(?i)upstream|circuit breaker|timeout|unavailable|too many requests|bad cast|internal error`)

// NewRPC returns an RPC client over the given nodes (own node first is best).
// Each argument may be a single URL or a comma-separated list.
func NewRPC(nodes ...string) *RPC {
	p := nodepool.New(nodepool.Sticky, nodes...)
	return &RPC{Nodes: p.Nodes(), HTTP: &http.Client{Timeout: 20 * time.Second}, pool: p}
}

// NewRoundRobinRPC is NewRPC that starts each call at the next node, for bulk
// reads whose answer is the same on every node (raw blocks): the load is
// spread so no single node rate-limits this IP.
func NewRoundRobinRPC(nodes ...string) *RPC {
	p := nodepool.New(nodepool.RoundRobin, nodes...)
	return &RPC{Nodes: p.Nodes(), HTTP: &http.Client{Timeout: 20 * time.Second}, pool: p}
}

// Call invokes a JSON-RPC method and unmarshals its result into out.
func (c *RPC) Call(ctx context.Context, method string, params, out any) error {
	return c.CallCheck(ctx, method, params, out, nil)
}

// CallCheck is Call with a check of the decoded answer: a node whose answer
// fails check (e.g. an empty account history from a node without the
// account_history plugin) is benched and the next node is asked.
func (c *RPC) CallCheck(ctx context.Context, method string, params, out any, check func() error) error {
	if c.pool == nil {
		c.pool = nodepool.New(nodepool.Sticky, c.Nodes...)
	}
	order := c.pool.Order()
	if len(order) == 0 {
		return errors.New("steemtx: no Steem RPC nodes configured")
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	var lastErr error
	for _, node := range order {
		result, err := c.post(ctx, node, body)
		var chainErr *ChainError
		if errors.As(err, &chainErr) {
			c.pool.OK(node) // the node works; the chain said no
			return err
		}
		if err == nil && out != nil {
			err = json.Unmarshal(result, out)
		}
		if err == nil && check != nil {
			err = check()
		}
		if err == nil {
			c.pool.OK(node)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.pool.Fail(node)
		lastErr = fmt.Errorf("%s: %w", node, err)
	}
	return lastErr
}

// post sends one request to one node. A transient node-side error or an
// unreadable reply is returned as a plain error (try the next node); a
// chain-level rejection as *ChainError.
func (c *RPC) post(ctx context.Context, node string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, node, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '<' {
			return nil, fmt.Errorf("HTML page instead of JSON (HTTP %d): the node is rate-limiting, blocking or down", resp.StatusCode)
		}
		return nil, fmt.Errorf("invalid response (HTTP %d): %w", resp.StatusCode, err)
	}
	if r.Error != nil {
		if transient.MatchString(r.Error.Message) {
			return nil, errors.New(r.Error.Message)
		}
		return nil, &ChainError{r.Error.Message}
	}
	return r.Result, nil
}
