package stablekit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
)

// Balance returns the raw token balance held by owner's Associated Token
// Account for mint. Returns 0 (no error) if the ATA does not yet exist.
func (c *Client) Balance(ctx context.Context, owner solana.PublicKey, mint Mint) (uint64, error) {
	mintPK, err := mint.PublicKey()
	if err != nil {
		return 0, fmt.Errorf("stablekit.Balance: parse mint: %w", err)
	}
	ataAddr, _, err := solana.FindAssociatedTokenAddress(owner, mintPK)
	if err != nil {
		return 0, fmt.Errorf("stablekit.Balance: derive ATA: %w", err)
	}

	out, err := c.rpc.GetTokenAccountBalance(ctx, ataAddr, rpc.CommitmentConfirmed)
	if err != nil {
		if isAccountNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stablekit.Balance: rpc: %w", err)
	}
	if out == nil || out.Value == nil {
		return 0, nil
	}
	return parseAmount(out.Value.Amount)
}

// ResolveATA derives the Associated Token Account address for owner+mint and
// reports whether it exists on-chain.
func (c *Client) ResolveATA(ctx context.Context, owner solana.PublicKey, mint Mint) (address solana.PublicKey, exists bool, err error) {
	mintPK, err := mint.PublicKey()
	if err != nil {
		return solana.PublicKey{}, false, fmt.Errorf("stablekit.ResolveATA: parse mint: %w", err)
	}
	ataAddr, _, err := solana.FindAssociatedTokenAddress(owner, mintPK)
	if err != nil {
		return solana.PublicKey{}, false, fmt.Errorf("stablekit.ResolveATA: derive: %w", err)
	}

	info, err := c.rpc.GetAccountInfoWithOpts(ctx, ataAddr, &rpc.GetAccountInfoOpts{
		Commitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		if isAccountNotFound(err) {
			return ataAddr, false, nil
		}
		return solana.PublicKey{}, false, fmt.Errorf("stablekit.ResolveATA: rpc: %w", err)
	}
	return ataAddr, info != nil && info.Value != nil, nil
}

// SendStable transfers SPL stablecoin from opts.From to opts.To. The sender
// pays the SOL fee. Auto-creates the destination ATA if opts.CreateDestATA
// is true.
func (c *Client) SendStable(ctx context.Context, opts SendOpts) (SendResult, error) {
	if opts.Amount == 0 {
		return SendResult{}, errors.New("stablekit.SendStable: Amount must be > 0")
	}
	mintPK, err := opts.Mint.PublicKey()
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: parse mint: %w", err)
	}

	srcATA, _, err := solana.FindAssociatedTokenAddress(opts.From, mintPK)
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: derive source ATA: %w", err)
	}
	dstATA, _, err := solana.FindAssociatedTokenAddress(opts.To, mintPK)
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: derive dest ATA: %w", err)
	}

	srcBal, err := c.rpc.GetTokenAccountBalance(ctx, srcATA, rpc.CommitmentConfirmed)
	if err != nil {
		if isAccountNotFound(err) {
			return SendResult{}, ErrSourceATAMissing
		}
		return SendResult{}, fmt.Errorf("stablekit.SendStable: source balance: %w", err)
	}
	if srcBal == nil || srcBal.Value == nil {
		return SendResult{}, ErrInsufficientBalance
	}
	srcAmt, err := parseAmount(srcBal.Value.Amount)
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: parse balance: %w", err)
	}
	if srcAmt < opts.Amount {
		return SendResult{}, ErrInsufficientBalance
	}

	createdDestATA := false
	dstInfo, err := c.rpc.GetAccountInfoWithOpts(ctx, dstATA, &rpc.GetAccountInfoOpts{Commitment: rpc.CommitmentConfirmed})
	dstExists := err == nil && dstInfo != nil && dstInfo.Value != nil
	if err != nil && !isAccountNotFound(err) {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: dest account info: %w", err)
	}
	if !dstExists {
		if !opts.CreateDestATA {
			return SendResult{}, ErrDestATAMissing
		}
		createdDestATA = true
	}

	var instructions []solana.Instruction
	if createdDestATA {
		ix, err := ata.NewCreateInstruction(opts.From, opts.To, mintPK).ValidateAndBuild()
		if err != nil {
			return SendResult{}, fmt.Errorf("stablekit.SendStable: build ATA-create: %w", err)
		}
		instructions = append(instructions, ix)
	}
	transferIx, err := token.NewTransferInstruction(opts.Amount, srcATA, dstATA, opts.From, nil).ValidateAndBuild()
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: build transfer: %w", err)
	}
	instructions = append(instructions, transferIx)

	blockhash, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: blockhash: %w", err)
	}
	tx, err := solana.NewTransaction(instructions, blockhash.Value.Blockhash, solana.TransactionPayer(opts.From))
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: build tx: %w", err)
	}
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(opts.From) {
			return &opts.FromSigner
		}
		return nil
	}); err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: sign: %w", err)
	}

	sig, err := c.rpc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		PreflightCommitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("stablekit.SendStable: send: %w", err)
	}

	return SendResult{Signature: sig, CreatedDestATA: createdDestATA}, nil
}

// GaslessTransfer transfers SPL stablecoin via Kora. Kora pays the SOL fee
// and broadcasts; the user only pays the fee in opts.FeeToken (defaults to
// opts.Mint). Returns the on-chain transaction signature.
func (c *Client) GaslessTransfer(ctx context.Context, opts GaslessTransferOpts) (string, error) {
	if c.kora == nil {
		return "", ErrKoraDisabled
	}
	if opts.Amount == 0 {
		return "", errors.New("stablekit.GaslessTransfer: Amount must be > 0")
	}
	mintPK, err := opts.Mint.PublicKey()
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: parse mint: %w", err)
	}
	feeToken := opts.FeeToken
	if feeToken == "" {
		feeToken = opts.Mint
	}

	sender := opts.SenderSigner.PublicKey()

	fromATA, _, err := solana.FindAssociatedTokenAddress(sender, mintPK)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: source ATA: %w", err)
	}
	toATA, _, err := solana.FindAssociatedTokenAddress(opts.Recipient, mintPK)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: dest ATA: %w", err)
	}

	payerResp, err := koraCall[koraGetPayerSignerResponse](ctx, c.kora, "getPayerSigner", nil)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: kora payer: %w", err)
	}
	koraPayer, err := solana.PublicKeyFromBase58(payerResp.SignerAddress)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: parse kora payer: %w", err)
	}

	transferIx := token.NewTransferInstruction(opts.Amount, fromATA, toATA, sender, nil).Build()

	recent, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: blockhash: %w", err)
	}

	tx, err := solana.NewTransaction(
		[]solana.Instruction{transferIx},
		recent.Value.Blockhash,
		solana.TransactionPayer(koraPayer),
	)
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: build tx: %w", err)
	}

	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: serialize: %w", err)
	}
	txBase64 := base64.StdEncoding.EncodeToString(txBytes)

	if _, err := koraCall[koraGetPaymentInstructionResponse](ctx, c.kora, "getPaymentInstruction", koraGetPaymentInstructionRequest{
		Transaction:  txBase64,
		FeeToken:     feeToken.String(),
		SourceWallet: sender.String(),
	}); err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: payment instruction: %w", err)
	}

	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(sender) {
			return &opts.SenderSigner
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: sign: %w", err)
	}

	signedBytes, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: serialize signed: %w", err)
	}

	resp, err := koraCall[koraSignAndSendResponse](ctx, c.kora, "signAndSendTransaction", koraSignAndSendRequest{
		Transaction: base64.StdEncoding.EncodeToString(signedBytes),
	})
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransfer: kora sign+send: %w", err)
	}
	return resp.Signature, nil
}

// GaslessTransferTx asks Kora to build a complete pre-signed transfer
// transaction (Kora's TransferTransaction RPC) and returns the base64
// transaction. Useful when you want Kora to handle the entire flow.
func (c *Client) GaslessTransferTx(ctx context.Context, opts GaslessTransferTxOpts) (string, error) {
	if c.kora == nil {
		return "", ErrKoraDisabled
	}
	resp, err := koraCall[koraTransferTransactionResponse](ctx, c.kora, "transferTransaction", koraTransferTransactionRequest{
		Source:      opts.Source,
		Destination: opts.Destination,
		Mint:        opts.Mint.String(),
		Amount:      opts.Amount,
	})
	if err != nil {
		return "", fmt.Errorf("stablekit.GaslessTransferTx: %w", err)
	}
	return resp.Transaction, nil
}

// Quote returns a Jupiter swap quote between two stablecoin mints.
func (c *Client) Quote(ctx context.Context, in, out Mint, amount uint64, slippageBps int) (QuoteResponse, error) {
	return c.jupiter.quote(ctx, in.String(), out.String(), amount, slippageBps)
}

// parseAmount parses an SPL token amount returned as a string.
func parseAmount(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", s, err)
	}
	return v, nil
}

// isAccountNotFound returns true for the "account does not exist" error.
func isAccountNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, needle := range []string{"could not find account", "AccountNotFound", "not found"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
