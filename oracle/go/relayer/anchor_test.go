package relayer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testAnchorBlock = 110435764
	testAnchorID    = "06951db4" + "abababababababababababababababab"
)

func TestAnchoredCursor(t *testing.T) {
	require.Equal(t, uint64(500), anchoredCursor(500, 0), "no anchor: cursor unchanged")
	require.Equal(t, uint64(testAnchorBlock-1), anchoredCursor(100, testAnchorBlock), "an old cursor jumps up to the anchor")
	require.Equal(t, uint64(testAnchorBlock-1), anchoredCursor(testAnchorBlock-1, testAnchorBlock))
	require.Equal(t, uint64(testAnchorBlock+9), anchoredCursor(testAnchorBlock+9, testAnchorBlock), "a cursor past the anchor is kept")
	// A local start-block override below the anchor is clamped too.
	require.Equal(t, uint64(testAnchorBlock-1), anchoredCursor(initialCursor(5, testAnchorBlock, 0), testAnchorBlock))
}

// fakeSteemRPC answers condenser_api.get_block with the given block ID and
// counts the calls.
func fakeSteemRPC(t *testing.T, blockID string, calls *int) *SteemClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "condenser_api.get_block", req.Method)
		*calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"block_id": blockID}})
	}))
	t.Cleanup(srv.Close)
	return NewSteemClient(srv.URL)
}

func TestSteemAnchorVerify(t *testing.T) {
	calls := 0
	good := fakeSteemRPC(t, testAnchorID, &calls)
	var a steemAnchor
	require.NoError(t, a.verify(good, testAnchorBlock, testAnchorID))
	require.NoError(t, a.verify(good, testAnchorBlock, testAnchorID))
	require.Equal(t, 1, calls, "verified once per process")

	require.NoError(t, (&steemAnchor{}).verify(good, 0, ""), "no recorded ID: nothing to verify")

	otherCalls := 0
	other := fakeSteemRPC(t, "06951db4"+"cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", &otherCalls)
	var b steemAnchor
	err := b.verify(other, testAnchorBlock, testAnchorID)
	require.ErrorContains(t, err, "steem anchor mismatch")
	require.ErrorContains(t, b.verify(other, testAnchorBlock, testAnchorID), "steem anchor mismatch", "a mismatch is never cached as verified")
}
