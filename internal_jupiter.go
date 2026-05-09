package stablekit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// jupiterConfig is the internal config for the bundled Jupiter HTTP client.
type jupiterConfig struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	maxRetries int
}

type jupiterClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	maxRetries int
}

func newJupiterClient(cfg jupiterConfig) *jupiterClient {
	return &jupiterClient{
		endpoint:   cfg.endpoint,
		apiKey:     cfg.apiKey,
		httpClient: cfg.httpClient,
		maxRetries: cfg.maxRetries,
	}
}

// quote fetches a Jupiter v6 quote.
func (j *jupiterClient) quote(ctx context.Context, in, out string, amount uint64, slippageBps int) (QuoteResponse, error) {
	q := url.Values{}
	q.Set("inputMint", in)
	q.Set("outputMint", out)
	q.Set("amount", strconv.FormatUint(amount, 10))
	if slippageBps > 0 {
		q.Set("slippageBps", strconv.Itoa(slippageBps))
	}

	var resp QuoteResponse
	if err := j.doJSON(ctx, http.MethodGet, j.endpoint+"/quote?"+q.Encode(), nil, &resp); err != nil {
		return QuoteResponse{}, err
	}
	return resp, nil
}

func (j *jupiterClient) doJSON(ctx context.Context, method, urlStr string, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("stablekit/jupiter: marshal: %w", err)
		}
		payload = b
	}

	var lastErr error
	for attempt := 0; attempt <= j.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return fmt.Errorf("stablekit/jupiter: %w", err)
			}
		}

		respBody, err := j.do(ctx, method, urlStr, payload)
		if err != nil {
			lastErr = err
			if isRetryable(err) {
				continue
			}
			return err
		}
		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("stablekit/jupiter: unmarshal: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("stablekit/jupiter: retries exhausted: %w", lastErr)
}

func (j *jupiterClient) do(ctx context.Context, method, urlStr string, payload []byte) ([]byte, error) {
	var rb io.Reader
	if payload != nil {
		rb = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rb)
	if err != nil {
		return nil, fmt.Errorf("stablekit/jupiter: new request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if j.apiKey != "" {
		req.Header.Set("x-api-key", j.apiKey)
	}

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return nil, &transportError{err: err, retryable: true}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stablekit/jupiter: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &transportError{
			err: &JupiterError{
				StatusCode: resp.StatusCode,
				Body:       string(body),
			},
			retryable: retryableStatus(resp.StatusCode),
		}
	}
	return body, nil
}

