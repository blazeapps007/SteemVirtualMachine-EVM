package relayer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// steemNode is a fake Steem node serving get_block up to head (null above it),
// counting the calls it receives.
type steemNode struct {
	mu    sync.Mutex
	head  uint64
	calls int
	html  bool
}

func (n *steemNode) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.calls++
		if n.html {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<!DOCTYPE html><html>403 Forbidden</html>"))
			return
		}
		var req struct {
			Params []uint64 `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var result any
		if req.Params[0] <= n.head {
			result = map[string]any{"timestamp": "2026-11-17T00:00:00", "transactions": []any{}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchBlocksSpreadsCallsOverNodes(t *testing.T) {
	a, b, c := &steemNode{head: 1000}, &steemNode{head: 1000}, &steemNode{head: 1000}
	client := NewSteemClient(a.serve(t) + "," + b.serve(t) + "," + c.serve(t))
	blocks, err := client.FetchBlocks(1, 90)
	require.NoError(t, err)
	require.Len(t, blocks, 90)
	require.Equal(t, []int{30, 30, 30}, []int{a.calls, b.calls, c.calls}, "round-robin keeps each node at a third of the load")
}

func TestFetchBlocksSkipsBlockedAndLaggingNodes(t *testing.T) {
	blocked := &steemNode{head: 1000, html: true}
	lagging := &steemNode{head: 10}
	good := &steemNode{head: 1000}
	client := NewSteemClient(blocked.serve(t), lagging.serve(t), good.serve(t))
	blocks, err := client.FetchBlocks(1, 50)
	require.NoError(t, err)
	require.Len(t, blocks, 50)
	require.Equal(t, 1, blocked.calls, "a node answering HTML is benched after one call")
	require.LessOrEqual(t, lagging.calls, 12, "a node missing a block is benched once it lacks one")
}

func TestFetchBlocksFailsWhenNoNodeHasTheBlock(t *testing.T) {
	a, b := &steemNode{head: 5}, &steemNode{head: 5}
	client := NewSteemClient(a.serve(t), b.serve(t))
	_, err := client.FetchBlocks(5, 6)
	require.ErrorContains(t, err, "no data for irreversible block 6")
	require.ErrorContains(t, err, "all 2 Steem nodes failed")
}

func TestSingleNodeErrorsUnchanged(t *testing.T) {
	n := &steemNode{html: true}
	_, err := NewSteemClient(n.serve(t)).LastIrreversibleBlock()
	require.ErrorContains(t, err, "steem rpc: invalid response: HTML page instead of JSON (HTTP 403)")
}
