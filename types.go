// Package stablekit is a focused Go SDK for stablecoin operations on Solana:
// balance queries, SPL transfers (regular and gasless via Kora), and Jupiter
// quotes. It wraps solana-go and bundles internal Kora JSON-RPC and Jupiter
// HTTP clients.
//
// Scope is intentionally narrow: stablecoin balance + send + quote.
// It is not a general-purpose Solana SDK — drop down to the underlying
// solana-go RPC when you need more.
package stablekit

import (
	"errors"
	"fmt"

	"github.com/gagliardetto/solana-go"
)

// Mint is the on-chain mint address of a stablecoin.
type Mint string

// String returns the mint address as a string.
func (m Mint) String() string { return string(m) }

// PublicKey parses the mint as a solana.PublicKey.
func (m Mint) PublicKey() (solana.PublicKey, error) {
	return solana.PublicKeyFromBase58(string(m))
}

// Common Solana stablecoin mints. Adding a constant here is a convenience —
// any base58 mint address works at the API surface.
const (
	USDC Mint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	USDT Mint = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
	EURC Mint = "HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr"
)

// SendOpts is the parameter for Client.SendStable.
type SendOpts struct {
	// From is the sender wallet (also the SOL fee payer).
	From solana.PublicKey
	// FromSigner signs the transaction. Must control the wallet at From.
	FromSigner solana.PrivateKey
	// To is the recipient owner pubkey (not an ATA).
	To solana.PublicKey
	// Mint identifies the stablecoin.
	Mint Mint
	// Amount is in the token's smallest unit (e.g. 1 USDC = 1_000_000).
	Amount uint64
	// CreateDestATA controls whether to add an ATA-creation instruction
	// when the recipient's ATA does not yet exist. When false, SendStable
	// returns ErrDestATAMissing.
	CreateDestATA bool
}

// SendResult is what Client.SendStable returns.
type SendResult struct {
	// Signature is the on-chain transaction signature.
	Signature solana.Signature
	// CreatedDestATA is true if the recipient's ATA was created in this tx.
	CreatedDestATA bool
}

// GaslessTransferOpts is the parameter for Client.GaslessTransfer.
//
// The user signs only their portion of the transaction; Kora signs as fee
// payer and broadcasts. The user pays the fee in the transfer token (or
// any token Kora supports), not in SOL.
type GaslessTransferOpts struct {
	// SenderSigner is the user's private key (also derives the source wallet).
	SenderSigner solana.PrivateKey
	// Recipient is the recipient owner pubkey (not an ATA).
	Recipient solana.PublicKey
	// Mint identifies the stablecoin.
	Mint Mint
	// Amount is in the token's smallest unit.
	Amount uint64
	// FeeToken is the mint that Kora will deduct the fee from. Defaults to
	// Mint when empty.
	FeeToken Mint
}

// GaslessTransferTxOpts is the parameter for Client.GaslessTransferTx —
// the Kora-built "simple transfer" path.
type GaslessTransferTxOpts struct {
	// Source is the sender wallet address.
	Source string
	// Destination is the recipient wallet address.
	Destination string
	// Mint identifies the stablecoin.
	Mint Mint
	// Amount is in the token's smallest unit.
	Amount uint64
}

// SwapOpts is the parameter for Client.Swap.
//
// Swap is a one-call helper that fetches a Jupiter quote, asks Jupiter to
// build a swap transaction, signs it with UserSigner, and submits it. The
// user pays the SOL fee. For "user has zero SOL" scenarios use the (yet to
// be added) GaslessSwap.
type SwapOpts struct {
	// UserSigner signs the swap tx and provides the user pubkey. Must hold
	// a balance of InputMint at the derived ATA.
	UserSigner solana.PrivateKey
	// InputMint is the source stablecoin mint.
	InputMint Mint
	// OutputMint is the destination stablecoin mint.
	OutputMint Mint
	// Amount is the input amount in the InputMint's smallest unit.
	Amount uint64
	// SlippageBps caps acceptable slippage in basis points (e.g. 10 = 0.10%).
	// Defaults to 50 if zero.
	SlippageBps int
	// WrapAndUnwrapSol asks Jupiter to auto-wrap SOL → wSOL and unwrap on
	// the way back. Only relevant if InputMint or OutputMint is wrapped SOL.
	// Defaults to true.
	WrapAndUnwrapSol *bool
}

// SwapResult is what Client.Swap returns.
type SwapResult struct {
	// Signature is the on-chain transaction signature.
	Signature solana.Signature
	// Quote is the Jupiter quote that was actually executed (handy for
	// reporting realized price / impact back to callers).
	Quote QuoteResponse
}

// QuoteResponse is the body returned by Jupiter's GET /quote.
type QuoteResponse struct {
	InputMint            string         `json:"inputMint"`
	InAmount             string         `json:"inAmount"`
	OutputMint           string         `json:"outputMint"`
	OutAmount            string         `json:"outAmount"`
	OtherAmountThreshold string         `json:"otherAmountThreshold"`
	SwapMode             string         `json:"swapMode"`
	SlippageBps          int            `json:"slippageBps"`
	PriceImpactPct       string         `json:"priceImpactPct"`
	RoutePlan            []routePlanHop `json:"routePlan"`
}

type routePlanHop struct {
	SwapInfo struct {
		AmmKey     string `json:"ammKey"`
		Label      string `json:"label,omitempty"`
		InputMint  string `json:"inputMint"`
		OutputMint string `json:"outputMint"`
		InAmount   string `json:"inAmount"`
		OutAmount  string `json:"outAmount"`
		FeeAmount  string `json:"feeAmount"`
		FeeMint    string `json:"feeMint"`
	} `json:"swapInfo"`
	Percent int `json:"percent"`
}

// JupiterError represents a non-2xx response from Jupiter.
type JupiterError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *JupiterError) Error() string {
	return fmt.Sprintf("stablekit: jupiter HTTP %d: %s", e.StatusCode, e.Body)
}

// KoraError represents an error returned by the Kora JSON-RPC server.
type KoraError struct {
	Code    int
	Message string
}

// Error implements the error interface.
func (e *KoraError) Error() string {
	return fmt.Sprintf("stablekit: kora rpc error %d: %s", e.Code, e.Message)
}

// Sentinel errors.
var (
	ErrSourceATAMissing    = errors.New("stablekit: source associated token account does not exist")
	ErrDestATAMissing      = errors.New("stablekit: destination associated token account does not exist")
	ErrInsufficientBalance = errors.New("stablekit: insufficient token balance")
	ErrKoraDisabled        = errors.New("stablekit: Kora is not configured (set Config.KoraEndpoint to enable gasless calls)")
)
