# stablekit

A focused Go SDK for stablecoin operations on Solana — balance queries, SPL transfers (regular and gasless via Kora), and Jupiter quotes.

Wraps [solana-go](https://github.com/gagliardetto/solana-go) and bundles internal Kora and Jupiter clients so callers only need a single import.

## Scope

Intentionally narrow — stablecoin send + balance + quote. Not a general-purpose Solana SDK; drop down to `client.RPC()` for anything outside this surface.

## Installation

```bash
go get github.com/rillehq/stablekit
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/gagliardetto/solana-go"
	"github.com/rillehq/stablekit"
)

func main() {
	c := stablekit.NewClient(stablekit.Config{
		RPCEndpoint:  "https://api.mainnet-beta.solana.com",
		KoraEndpoint: "https://kora.example.com",
		KoraAPIKey:   "...",
	})
	ctx := context.Background()

	owner := solana.MustPublicKeyFromBase58("...")

	// Balance
	bal, err := c.Balance(ctx, owner, stablekit.USDC)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("USDC balance: %d\n", bal)

	// Gasless transfer (Kora pays SOL fee)
	sig, err := c.GaslessTransfer(ctx, stablekit.GaslessTransferOpts{
		SenderSigner: solana.MustPrivateKeyFromBase58("..."),
		Recipient:    solana.MustPublicKeyFromBase58("..."),
		Mint:         stablekit.USDC,
		Amount:       1_000_000, // 1 USDC
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Sent:", sig)

	// Jupiter quote (USDT → USDC)
	quote, err := c.Quote(ctx, stablekit.USDT, stablekit.USDC, 1_000_000, 10)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Out:", quote.OutAmount, "Impact:", quote.PriceImpactPct)

	// Execute the swap (user pays SOL fee)
	res, err := c.Swap(ctx, stablekit.SwapOpts{
		UserSigner:  solana.MustPrivateKeyFromBase58("..."),
		InputMint:   stablekit.USDT,
		OutputMint:  stablekit.USDC,
		Amount:      1_000_000,
		SlippageBps: 10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Swap signature:", res.Signature)
}
```

## API surface

| Method                 | Purpose                                                                |
|------------------------|------------------------------------------------------------------------|
| `Balance`              | SPL token balance for an owner+mint (returns 0 if ATA missing)         |
| `ResolveATA`           | Derive the owner's ATA address; report whether it exists on-chain      |
| `SendStable`           | SPL transfer where the sender pays the SOL fee                         |
| `GaslessTransfer`      | SPL transfer where Kora pays the SOL fee (user pays in token)          |
| `GaslessTransferTx`    | Kora-built simple transfer (returns base64 transaction)                |
| `Quote`                | Jupiter v6 quote (USDT↔USDC etc.)                                      |
| `Swap`                 | Quote + execute swap in one call (user pays SOL fee)                   |
| `RPC()`                | Underlying solana-go RPC client (escape hatch)                         |
| `KoraEnabled()`        | Whether Kora was configured                                            |

## Configuration

| Field             | Description                                                         | Default                       |
|-------------------|---------------------------------------------------------------------|-------------------------------|
| `RPCEndpoint`     | Solana JSON-RPC URL (required)                                      | —                             |
| `KoraEndpoint`    | Kora fee-abstraction URL. Empty = gasless calls return ErrKoraDisabled | —                          |
| `KoraAPIKey`      | `x-api-key` header for Kora                                         | —                             |
| `KoraHMACSecret`  | HMAC secret for `x-hmac-signature` per Kora request                 | —                             |
| `JupiterEndpoint` | Override Jupiter v6 URL                                             | `https://quote-api.jup.ag/v6` |
| `JupiterAPIKey`   | `x-api-key` for paid Jupiter tiers                                  | —                             |
| `HTTPClient`      | Shared `*http.Client`                                               | 15s timeout                   |
| `MaxRetries`      | Retries on 429 / 5xx / transport errors                             | 3                             |

## Errors

```go
import "errors"

_, err := c.GaslessTransfer(ctx, opts)
if errors.Is(err, stablekit.ErrKoraDisabled) {
	// Kora not configured
}

var ke *stablekit.KoraError
if errors.As(err, &ke) {
	fmt.Printf("kora rpc error %d: %s\n", ke.Code, ke.Message)
}

var je *stablekit.JupiterError
if errors.As(err, &je) {
	fmt.Printf("jupiter http %d: %s\n", je.StatusCode, je.Body)
}
```

## License

MIT
