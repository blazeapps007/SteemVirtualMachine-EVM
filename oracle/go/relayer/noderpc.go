package relayer

import (
	"net/http"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

// nodeRPCIdleTimeout is how long an idle connection to the node is kept for
// reuse. It must stay below the CometBFT RPC server's idle timeout: the server
// sets no IdleTimeout, so Go closes idle connections after its ReadTimeout,
// 10s by default. The stock client keeps them forever, so a caller polling
// about every 10s (the svm.bank signer) keeps reusing a connection just as the
// server closes it — "EOF" / "connection reset by peer".
const nodeRPCIdleTimeout = 5 * time.Second

// NewNodeRPC is a CometBFT RPC client for the SteemVM node whose idle
// connections are dropped before the server drops them.
func NewNodeRPC(remote string) (*rpchttp.HTTP, error) {
	return newNodeRPC(remote, nodeRPCIdleTimeout)
}

func newNodeRPC(remote string, idle time.Duration) (*rpchttp.HTTP, error) {
	return rpchttp.NewWithClient(remote, "/websocket", &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
			IdleConnTimeout:    idle,
		},
	})
}
