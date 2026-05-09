package stablekit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// koraConfig is the internal config for the bundled Kora JSON-RPC client.
type koraConfig struct {
	endpoint   string
	apiKey     string
	hmacSecret string
	httpClient *http.Client
	maxRetries int
}

// koraClient is an internal JSON-RPC client for the Kora fee abstraction layer.
type koraClient struct {
	endpoint   string
	apiKey     string
	hmacSecret string
	httpClient *http.Client
	maxRetries int
	idCounter  atomic.Uint64
}

func newKoraClient(cfg koraConfig) *koraClient {
	return &koraClient{
		endpoint:   cfg.endpoint,
		apiKey:     cfg.apiKey,
		hmacSecret: cfg.hmacSecret,
		httpClient: cfg.httpClient,
		maxRetries: cfg.maxRetries,
	}
}

// --- JSON-RPC envelope ---

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      uint64      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- Method-specific types (internal) ---

type koraGetPaymentInstructionRequest struct {
	Transaction  string `json:"transaction"`
	FeeToken     string `json:"fee_token"`
	SourceWallet string `json:"source_wallet"`
}

type koraGetPaymentInstructionResponse struct {
	PaymentInstruction string `json:"payment_instruction"`
	PaymentAmount     uint64 `json:"payment_amount"`
}

type koraSignAndSendRequest struct {
	Transaction string `json:"transaction"`
}

type koraSignAndSendResponse struct {
	Signature         string `json:"signature"`
	SignedTransaction string `json:"signed_transaction"`
	SignerPubkey      string `json:"signer_pubkey"`
}

type koraTransferTransactionRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mint        string `json:"mint,omitempty"`
	Amount      uint64 `json:"amount"`
}

type koraTransferTransactionResponse struct {
	Transaction  string `json:"transaction"`
	SignerPubkey string `json:"signer_pubkey"`
}

type koraGetPayerSignerResponse struct {
	SignerAddress string `json:"signer_address"`
}

// --- Method dispatch ---

func koraCall[T any](ctx context.Context, c *koraClient, method string, params any) (T, error) {
	var zero T

	id := c.idCounter.Add(1)
	payload, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return zero, fmt.Errorf("stablekit/kora.%s: marshal: %w", method, err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return zero, fmt.Errorf("stablekit/kora.%s: %w", method, err)
			}
		}

		result, err := c.do(ctx, method, payload)
		if err != nil {
			lastErr = err
			if isRetryable(err) {
				continue
			}
			return zero, err
		}

		var out T
		if err := json.Unmarshal(result, &out); err != nil {
			return zero, fmt.Errorf("stablekit/kora.%s: unmarshal: %w", method, err)
		}
		return out, nil
	}

	return zero, fmt.Errorf("stablekit/kora.%s: retries exhausted: %w", method, lastErr)
}

func (c *koraClient) do(ctx context.Context, method string, payload []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("stablekit/kora.%s: new request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
	if c.hmacSecret != "" {
		mac := hmac.New(sha256.New, []byte(c.hmacSecret))
		mac.Write(payload)
		req.Header.Set("x-hmac-signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &transportError{err: err, retryable: true}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stablekit/kora.%s: read body: %w", method, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &transportError{
			err:       fmt.Errorf("kora HTTP %d: %s", resp.StatusCode, string(body)),
			retryable: retryableStatus(resp.StatusCode),
		}
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("stablekit/kora.%s: unmarshal envelope: %w", method, err)
	}
	if rpcResp.Error != nil {
		return nil, &KoraError{Code: rpcResp.Error.Code, Message: rpcResp.Error.Message}
	}
	return rpcResp.Result, nil
}
