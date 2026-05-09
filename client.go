package stablekit

import (
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go/rpc"
)

// Default Jupiter v6 endpoint.
const defaultJupiterEndpoint = "https://quote-api.jup.ag/v6"

// Config holds the configuration for a stablekit Client.
type Config struct {
	// RPCEndpoint is a Solana JSON-RPC endpoint URL (required).
	// Example: https://api.mainnet-beta.solana.com
	RPCEndpoint string

	// KoraEndpoint is the Kora fee-abstraction JSON-RPC URL.
	// Optional. When unset, GaslessTransfer/GaslessTransferTx return ErrKoraDisabled.
	KoraEndpoint string
	// KoraAPIKey is sent as the x-api-key header when calling Kora.
	KoraAPIKey string
	// KoraHMACSecret signs each Kora request as x-hmac-signature.
	KoraHMACSecret string

	// JupiterEndpoint overrides the default Jupiter v6 endpoint
	// (https://quote-api.jup.ag/v6).
	JupiterEndpoint string
	// JupiterAPIKey is an optional Jupiter API key for paid tiers.
	JupiterAPIKey string

	// HTTPClient is shared by Kora and Jupiter clients. Defaults to a 15s
	// timeout client.
	HTTPClient *http.Client
	// MaxRetries on transient errors (5xx, 429, transport). Defaults to 3.
	MaxRetries int
}

// Client is a thread-safe stablecoin SDK over solana-go, with Kora and
// Jupiter clients bundled.
type Client struct {
	rpc     *rpc.Client
	kora    *koraClient    // nil if KoraEndpoint is empty
	jupiter *jupiterClient // always non-nil
}

// NewClient creates a stablekit Client. Panics if RPCEndpoint is empty.
func NewClient(cfg Config) *Client {
	if cfg.RPCEndpoint == "" {
		panic("stablekit: Config.RPCEndpoint is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	retries := cfg.MaxRetries
	if retries <= 0 {
		retries = 3
	}

	c := &Client{
		rpc: rpc.New(cfg.RPCEndpoint),
		jupiter: newJupiterClient(jupiterConfig{
			endpoint:   firstNonEmpty(cfg.JupiterEndpoint, defaultJupiterEndpoint),
			apiKey:     cfg.JupiterAPIKey,
			httpClient: hc,
			maxRetries: retries,
		}),
	}

	if cfg.KoraEndpoint != "" {
		c.kora = newKoraClient(koraConfig{
			endpoint:   cfg.KoraEndpoint,
			apiKey:     cfg.KoraAPIKey,
			hmacSecret: cfg.KoraHMACSecret,
			httpClient: hc,
			maxRetries: retries,
		})
	}

	return c
}

// RPC returns the underlying solana-go RPC client. Use this when stablekit
// does not expose an operation directly. The returned client is shared —
// do not Close it.
func (c *Client) RPC() *rpc.Client { return c.rpc }

// KoraEnabled reports whether the Kora client was configured.
func (c *Client) KoraEnabled() bool { return c.kora != nil }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
