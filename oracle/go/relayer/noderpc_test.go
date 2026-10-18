package relayer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
)

func TestNodeRPCIdleTimeoutBelowServer(t *testing.T) {
	if server := rpcserver.DefaultConfig().ReadTimeout; nodeRPCIdleTimeout >= server {
		t.Fatalf("idle timeout %v must be below the CometBFT RPC server's %v", nodeRPCIdleTimeout, server)
	}
}

// The race, scaled down: a server that drops idle connections after 200ms
// (like CometBFT's: no IdleTimeout, so ReadTimeout) and a client polling
// every 200ms. With its idle timeout below the server's, no poll may fail.
func TestNodeRPCSurvivesServerIdleClose(t *testing.T) {
	const serverIdle = 200 * time.Millisecond
	srv := &http.Server{
		ReadTimeout: serverIdle,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client, err := newNodeRPC("http://"+ln.Addr().String(), serverIdle/2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		if _, err := client.Health(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		time.Sleep(serverIdle)
	}
}
