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
	"net/http"
	"regexp"
	"strings"
	"time"
)

// RPC calls Steem nodes in order, moving to the next one only on transport
// errors. A chain-level rejection is returned immediately (another node would
// reject it too).
type RPC struct {
	Nodes []string
	HTTP  *http.Client
}

// ChainError is a rejection from the Steem chain itself (not a transport
// problem); it is never retried on another node.
type ChainError struct{ Message string }

func (e *ChainError) Error() string { return e.Message }

// Node-side errors worth trying the next node for. "Bad Cast" comes from a
// misbehaving backend behind a load balancer (api.steemit.com), not from
// Steem itself; the same call succeeds on the next node or a moment later.
var transient = regexp.MustCompile(`(?i)upstream|circuit breaker|timeout|unavailable|too many requests|bad cast`)

// NewRPC returns an RPC client over the given nodes (own node first is best).
func NewRPC(nodes ...string) *RPC {
	clean := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n = strings.TrimSpace(n); n != "" {
			clean = append(clean, n)
		}
	}
	return &RPC{Nodes: clean, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Call invokes a JSON-RPC method and unmarshals its result into out.
func (c *RPC) Call(ctx context.Context, method string, params, out any) error {
	if len(c.Nodes) == 0 {
		return errors.New("steemtx: no Steem RPC nodes configured")
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	var lastErr error
	for _, node := range c.Nodes {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, node, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var r struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", node, err)
			continue
		}
		if r.Error != nil {
			if transient.MatchString(r.Error.Message) {
				lastErr = fmt.Errorf("%s: %s", node, r.Error.Message)
				continue
			}
			return &ChainError{r.Error.Message}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(r.Result, out)
	}
	return lastErr
}
