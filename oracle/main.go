// Command steem-oracle is the standalone SteemVM bridge oracle.
//
// It watches the Steem blockchain for transfers to the on-chain gateway
// account and, when its configured key belongs to a bonded validator,
// broadcasts the matching bridge-deposit / name-registration attestations to a
// SteemVM node. It is the same relayer logic that used to run inside steemvmd,
// now split out so operators run it as a separate container with their signing
// key supplied via the environment (never baked into the chain binary or its
// app.toml).
//
// Configuration is entirely from environment variables:
//
//	ORACLE_STEEM_RPC      (required) Steem RPC to scan, e.g. https://api.steemit.com
//	ORACLE_NODE_RPC       SteemVM CometBFT RPC to broadcast to  (default http://steemvm:26657)
//	ORACLE_PRIVATE_KEY    validator key as hex (0x… eth_secp256k1)  ─┐ one of these
//	ORACLE_MNEMONIC       validator key as a BIP39 mnemonic          ─┘ is required
//	ORACLE_STATE_DIR      dir for the scan-cursor state file    (default /oracle-data)
//	ORACLE_POLL_INTERVAL  Steem poll cadence                    (default 1m)
//	ORACLE_MAX_BLOCKS     max Steem blocks scanned per cycle     (default 100)
//	ORACLE_START_BLOCK    fresh-scan start: a block number, or "latest" to start
//	                      at Steem's current tip (new validators skip all history)
//	                      (default 0 = use the chain's relayer_start_block anchor)
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cosmossdk.io/depinject"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/config"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtxconfig "github.com/cosmos/cosmos-sdk/x/auth/tx/config"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	ethhd "github.com/cosmos/evm/crypto/hd"
	cosmosevmkeyring "github.com/cosmos/evm/crypto/keyring"

	"steemvm/app"
	"steemvm/relayer"
)

const keyName = "oracle"

func main() {
	logger := log.NewLogger(os.Stdout).With("module", "steem-oracle")

	if err := run(logger); err != nil {
		logger.Error("oracle exited", "err", err)
		os.Exit(1)
	}
}

func run(logger log.Logger) error {
	steemRPC := os.Getenv("ORACLE_STEEM_RPC")
	if steemRPC == "" {
		return fmt.Errorf("ORACLE_STEEM_RPC is required (the Steem RPC to scan)")
	}
	nodeRPC := envOr("ORACLE_NODE_RPC", "http://steemvm:26657")
	stateDir := envOr("ORACLE_STATE_DIR", "/oracle-data")

	cfg := relayer.DefaultConfig()
	cfg.KeyName = keyName
	cfg.SteemRPCURL = steemRPC
	// SteemSymbol stays "STEEM" (DefaultConfig) — the oracle watches Steem
	// mainnet, where the bridgeable asset is always "STEEM". Not configurable.
	if d, ok, err := envDuration("ORACLE_POLL_INTERVAL"); err != nil {
		return err
	} else if ok {
		cfg.PollInterval = d
	}
	if n, ok, err := envUint("ORACLE_MAX_BLOCKS"); err != nil {
		return err
	} else if ok {
		cfg.MaxBlocksPerPoll = n
	}
	// ORACLE_START_BLOCK: where a *fresh* oracle (no saved cursor yet) begins its
	// Steem scan. A block number starts there; "latest" starts at Steem's current
	// last-irreversible block — so a new validator skips all history and only
	// attests deposits from now on. Empty/0 defers to the chain's
	// relayer_start_block anchor. (Ignored once a cursor exists in the state dir.)
	switch raw := strings.ToLower(strings.TrimSpace(os.Getenv("ORACLE_START_BLOCK"))); raw {
	case "", "0":
		// leave cfg.StartBlock = 0 → use on-chain relayer_start_block, else tip
	case "latest", "now", "tip":
		lib, err := relayer.NewSteemClient(cfg.SteemRPCURL).LastIrreversibleBlock()
		if err != nil {
			return fmt.Errorf("resolving ORACLE_START_BLOCK=%q: %w", raw, err)
		}
		cfg.StartBlock = lib
		logger.Info("oracle will start from the current Steem tip", "start_block", lib)
	default:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("ORACLE_START_BLOCK must be a block number or \"latest\": %w", err)
		}
		cfg.StartBlock = n
	}

	// Build the app codec / tx config (registers eth_secp256k1 and the
	// steembridge messages so attestation txs sign and encode correctly).
	clientCtx, err := newClientContext()
	if err != nil {
		return fmt.Errorf("building client context: %w", err)
	}

	// Import the signing key from the environment into an in-memory keyring.
	kr, err := buildKeyring(clientCtx.Codec)
	if err != nil {
		return err
	}
	clientCtx = clientCtx.WithKeyring(kr)

	rec, err := kr.Key(keyName)
	if err != nil {
		return fmt.Errorf("loading imported key: %w", err)
	}
	addr, err := rec.GetAddress()
	if err != nil {
		return fmt.Errorf("reading key address: %w", err)
	}
	logger.Info("oracle key loaded", "address", addr.String(), "node_rpc", nodeRPC, "steem_rpc", steemRPC)

	// Cancel cleanly on SIGINT/SIGTERM so the container stops promptly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return relayer.Run(ctx, logger, clientCtx, nodeRPC, cfg, stateDir)
}

// newClientContext assembles a client.Context with the full SteemVM codec via
// the app's dependency-injection config — the same wiring steemvmd uses — so
// the oracle can sign eth_secp256k1 transactions and marshal steembridge
// messages without embedding the whole node.
func newClientContext() (client.Context, error) {
	var clientCtx client.Context
	if err := depinject.Inject(
		depinject.Configs(
			app.AppConfig(),
			depinject.Supply(log.NewNopLogger()),
			depinject.Provide(ProvideClientContext),
			depinject.Provide(app.ProvideMsgEthereumTxCustomGetSigner),
		),
		&clientCtx,
	); err != nil {
		return client.Context{}, err
	}

	// IBC and EVM modules aren't dependency-injected; register their interfaces
	// on the codec manually (mirrors cmd/steemvmd's root command) so all pubkey
	// and message types the tx may carry are known.
	app.RegisterIBC(clientCtx.Codec)
	app.RegisterEVM(clientCtx.Codec, clientCtx.InterfaceRegistry)

	return clientCtx, nil
}

func ProvideClientContext(
	appCodec codec.Codec,
	interfaceRegistry codectypes.InterfaceRegistry,
	txConfigOpts tx.ConfigOptions,
	legacyAmino *codec.LegacyAmino,
) client.Context {
	clientCtx := client.Context{}.
		WithCodec(appCodec).
		WithInterfaceRegistry(interfaceRegistry).
		WithLegacyAmino(legacyAmino).
		WithInput(os.Stdin).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithHomeDir(app.DefaultNodeHome).
		WithViper(app.Name).
		WithKeyringOptions(cosmosevmkeyring.Option()).
		WithLedgerHasProtobuf(true)

	clientCtx, _ = config.ReadFromClientConfig(clientCtx)

	txConfigOpts.TextualCoinMetadataQueryFn = authtxconfig.NewGRPCCoinMetadataQueryFn(clientCtx)
	txConfig, err := tx.NewTxConfigWithOptions(clientCtx.Codec, txConfigOpts)
	if err != nil {
		panic(err)
	}
	return clientCtx.WithTxConfig(txConfig)
}

// buildKeyring creates an in-memory keyring (with eth_secp256k1 support) and
// imports the operator key from ORACLE_PRIVATE_KEY (hex) or ORACLE_MNEMONIC.
func buildKeyring(cdc codec.Codec) (keyring.Keyring, error) {
	kr := keyring.NewInMemory(cdc, cosmosevmkeyring.Option())

	privHex := strings.TrimSpace(os.Getenv("ORACLE_PRIVATE_KEY"))
	mnemonic := strings.TrimSpace(os.Getenv("ORACLE_MNEMONIC"))

	switch {
	case privHex != "":
		privHex = strings.TrimPrefix(privHex, "0x")
		if _, err := hex.DecodeString(privHex); err != nil {
			return nil, fmt.Errorf("ORACLE_PRIVATE_KEY is not valid hex: %w", err)
		}
		if err := kr.ImportPrivKeyHex(keyName, privHex, "eth_secp256k1"); err != nil {
			return nil, fmt.Errorf("importing ORACLE_PRIVATE_KEY: %w", err)
		}
	case mnemonic != "":
		hdPath := hd.CreateHDPath(app.ChainCoinType, 0, 0).String()
		if _, err := kr.NewAccount(keyName, mnemonic, "", hdPath, ethhd.EthSecp256k1); err != nil {
			return nil, fmt.Errorf("deriving key from ORACLE_MNEMONIC: %w", err)
		}
	default:
		return nil, fmt.Errorf("provide the signing key via ORACLE_PRIVATE_KEY (hex) or ORACLE_MNEMONIC")
	}
	return kr, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envDuration(key string) (time.Duration, bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false, fmt.Errorf("%s is not a valid duration (e.g. 3s): %w", key, err)
	}
	return d, true, nil
}

func envUint(key string) (uint64, bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%s is not a valid unsigned integer: %w", key, err)
	}
	return n, true, nil
}
