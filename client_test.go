package stablekit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
)

// solanaKeypair returns a fresh keypair for tests that need a signer.
func solanaKeypair() (solana.PrivateKey, solana.PublicKey) {
	k, err := solana.NewRandomPrivateKey()
	if err != nil {
		panic(err)
	}
	return k, k.PublicKey()
}

func newTestClient(t *testing.T, koraHandler, jupiterHandler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	var koraURL string
	if koraHandler != nil {
		s := httptest.NewServer(koraHandler)
		koraURL = s.URL
		t.Cleanup(s.Close)
	}
	jupURL := ""
	if jupiterHandler != nil {
		s := httptest.NewServer(jupiterHandler)
		jupURL = s.URL
		t.Cleanup(s.Close)
	}
	cfg := Config{
		RPCEndpoint:     "http://localhost:8899", // not exercised in unit tests
		KoraEndpoint:    koraURL,
		JupiterEndpoint: jupURL,
		HTTPClient:      &http.Client{Timeout: 2 * time.Second},
		MaxRetries:      1,
	}
	c := NewClient(cfg)
	return c, func() {}
}

func TestNewClient_RequiresRPC(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for empty RPCEndpoint")
		}
	}()
	NewClient(Config{})
}

func TestKoraDisabled_WhenEndpointEmpty(t *testing.T) {
	c := NewClient(Config{RPCEndpoint: "http://localhost:8899"})
	if c.KoraEnabled() {
		t.Fatal("expected KoraEnabled to be false when KoraEndpoint is empty")
	}
	_, err := c.GaslessTransferTx(context.Background(), GaslessTransferTxOpts{
		Source:      "src",
		Destination: "dst",
		Mint:        USDC,
		Amount:      1,
	})
	if err != ErrKoraDisabled {
		t.Fatalf("expected ErrKoraDisabled, got %v", err)
	}
}

func TestQuote_Success(t *testing.T) {
	c, _ := newTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/quote") {
			t.Fatalf("expected /quote, got %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("inputMint") != string(USDT) || q.Get("outputMint") != string(USDC) {
			t.Fatalf("missing mints: %v", q)
		}
		if q.Get("amount") != "1000000" || q.Get("slippageBps") != "10" {
			t.Fatalf("unexpected params: %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"inputMint":"`+string(USDT)+`",
			"inAmount":"1000000",
			"outputMint":"`+string(USDC)+`",
			"outAmount":"999500",
			"otherAmountThreshold":"998500",
			"swapMode":"ExactIn",
			"slippageBps":10,
			"priceImpactPct":"0.0001",
			"routePlan":[]
		}`)
	})
	got, err := c.Quote(context.Background(), USDT, USDC, 1_000_000, 10)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if got.OutAmount != "999500" {
		t.Fatalf("expected outAmount=999500, got %s", got.OutAmount)
	}
}

func TestGaslessTransferTx_Success(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string                          `json:"method"`
			Params koraTransferTransactionRequest `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode kora req: %v", err)
		}
		if req.Method != "transferTransaction" {
			t.Fatalf("expected transferTransaction, got %s", req.Method)
		}
		if req.Params.Mint != string(USDC) {
			t.Fatalf("expected mint=USDC, got %s", req.Params.Mint)
		}
		if req.Params.Amount != 1_000_000 {
			t.Fatalf("expected amount=1_000_000, got %d", req.Params.Amount)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"transaction":"BASE64TX","signer_pubkey":"P"}}`)
	}, nil)

	tx, err := c.GaslessTransferTx(context.Background(), GaslessTransferTxOpts{
		Source:      "src-pk",
		Destination: "dst-pk",
		Mint:        USDC,
		Amount:      1_000_000,
	})
	if err != nil {
		t.Fatalf("GaslessTransferTx: %v", err)
	}
	if tx != "BASE64TX" {
		t.Fatalf("expected BASE64TX, got %s", tx)
	}
}

func TestKoraError_Surfaced(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad mint"}}`)
	}, nil)

	_, err := c.GaslessTransferTx(context.Background(), GaslessTransferTxOpts{
		Source: "s", Destination: "d", Mint: USDC, Amount: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad mint") || !strings.Contains(err.Error(), "-32602") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSwap_BuildsTxFromJupiter(t *testing.T) {
	// We mock the Jupiter side fully and verify stablekit drives the
	// quote → swap RPC sequence correctly. We cannot exercise on-chain
	// signing/sending in a unit test (that needs a real or mock Solana
	// RPC), so the test stops short of solana.TransactionFromBytes.
	calls := []string{}
	c, _ := newTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/quote"):
			_, _ = io.WriteString(w, `{
				"inputMint":"`+string(USDT)+`",
				"inAmount":"1000000",
				"outputMint":"`+string(USDC)+`",
				"outAmount":"999500",
				"otherAmountThreshold":"998500",
				"swapMode":"ExactIn",
				"slippageBps":10,
				"priceImpactPct":"0.0001",
				"routePlan":[]
			}`)
		case r.URL.Path == "/swap":
			var req jupiterSwapRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode swap req: %v", err)
			}
			if req.QuoteResponse.OutAmount != "999500" {
				t.Fatalf("quote not threaded through: %+v", req.QuoteResponse)
			}
			if req.UserPublicKey == "" {
				t.Fatal("missing userPublicKey")
			}
			// Return a deliberately-invalid base64 so we exercise the path
			// up to the decode error without needing a real signed tx.
			_, _ = io.WriteString(w, `{"swapTransaction":"not-base64!!!","lastValidBlockHeight":12345}`)
		}
	})

	signer, _ := solanaKeypair()
	_, err := c.Swap(context.Background(), SwapOpts{
		UserSigner:  signer,
		InputMint:   USDT,
		OutputMint:  USDC,
		Amount:      1_000_000,
		SlippageBps: 10,
	})
	if err == nil {
		t.Fatal("expected decode error from invalid base64, got nil")
	}
	if !strings.Contains(err.Error(), "decode swap tx") {
		t.Fatalf("expected decode error, got: %v", err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "/quote") || calls[1] != "/swap" {
		t.Fatalf("expected /quote then /swap, got %v", calls)
	}
}

func TestRetry_OnServerError(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"inputMint":"a","outputMint":"b","inAmount":"1","outAmount":"1","otherAmountThreshold":"1","swapMode":"ExactIn","slippageBps":0,"priceImpactPct":"0","routePlan":[]}`)
	})
	_, err := c.Quote(context.Background(), Mint("a"), Mint("b"), 1, 0)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (1 retry), got %d", calls)
	}
}
