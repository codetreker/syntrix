package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/trigger/types"
)

// HTTPClientOptions configures the HTTP client.
type HTTPClientOptions struct {
	// Timeout is an optional total HTTP cap. Zero relies on the caller's context.
	Timeout time.Duration
}

// HTTPWorker handles the execution of delivery tasks via HTTP.
type HTTPWorker struct {
	client  *http.Client
	auth    identity.SystemTokenIssuer
	secrets SecretProvider
	metrics types.Metrics
}

// NewDeliveryWorker creates a new HTTPWorker.
func NewDeliveryWorker(auth identity.SystemTokenIssuer, secrets SecretProvider, opts HTTPClientOptions, metrics types.Metrics) DeliveryWorker {
	if metrics == nil {
		metrics = &types.NoopMetrics{}
	}
	return &HTTPWorker{
		client: &http.Client{
			Timeout: opts.Timeout,
		},
		auth:    auth,
		secrets: secrets,
		metrics: metrics,
	}
}

// ProcessTask executes one attempt using the caller's context deadline.
// The task timeout is applied by the consumer before invoking the worker.
func (w *HTTPWorker) ProcessTask(ctx context.Context, task *types.DeliveryTask) error {
	start := time.Now()
	// Add System Token
	if w.auth != nil {
		token, err := w.auth.GenerateSystemToken("trigger-worker")
		if err != nil {
			w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, false)
			return fmt.Errorf("failed to generate system token: %w", err)
		}
		task.PreIssuedToken = token
	}

	payload, err := json.Marshal(task)
	if err != nil {
		w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, true)
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", task.URL, bytes.NewReader(payload))
	if err != nil {
		w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, true)
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add Headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Syntrix-Trigger-Service/1.0")
	for k, v := range task.Headers {
		req.Header.Set(k, v)
	}

	// Add Signature (only if SecretsRef is configured)
	if task.SecretsRef != "" {
		if w.secrets == nil {
			log.Printf("[Warning] SecretsRef %s specified but no SecretProvider configured", task.SecretsRef)
			w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, true)
			return &types.FatalError{Err: fmt.Errorf("no secret provider configured for SecretsRef %s", task.SecretsRef)}
		}
		secret, err := w.secrets.GetSecret(ctx, task.SecretsRef)
		if err != nil {
			// If we can't get the secret, should we fail fatally or retry?
			// Probably retry, as it might be a temporary issue with secret store.
			w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, false)
			return fmt.Errorf("failed to resolve secret %s: %w", task.SecretsRef, err)
		}
		timestamp := time.Now().Unix()
		signature := w.signPayload(payload, secret, timestamp)
		req.Header.Set("X-Syntrix-Signature", signature)
	}
	// If SecretsRef is empty, skip signature header entirely (webhook may not require it)

	resp, err := w.client.Do(req)
	if err != nil {
		w.metrics.IncDeliveryFailure(task.Database, task.Collection, 0, false)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		w.metrics.IncDeliverySuccess(task.Database, task.Collection)
		w.metrics.ObserveDeliveryLatency(task.Database, task.Collection, time.Since(start))
		return nil
	}

	fatal := resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests
	var deliveryErr error = fmt.Errorf("webhook failed with status: %d", resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests {
		if values := resp.Header.Values("Retry-After"); len(values) == 1 {
			delay, parseErr := parseRetryAfter(values[0], time.Now())
			if parseErr != nil {
				fatal = true
				deliveryErr = fmt.Errorf("%w: %w", deliveryErr, parseErr)
			} else if delay > 0 {
				deliveryErr = &types.RetryAfterError{Err: deliveryErr, Delay: delay}
			}
		}
	}
	w.metrics.IncDeliveryFailure(task.Database, task.Collection, resp.StatusCode, fatal)

	if fatal {
		return &types.FatalError{Err: deliveryErr}
	}

	return deliveryErr
}

func (w *HTTPWorker) signPayload(body []byte, secret string, timestamp int64) string {
	// Signature format: t={ts},v1={hex(hmac)}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", timestamp, sig)
}

// Invalid hints leave rule backoff in control. A valid but unrepresentable delay
// is reported separately so it cannot overflow into an immediate retry.
func parseRetryAfter(value string, now time.Time) (time.Duration, error) {
	value = strings.Trim(value, " \t")
	if value == "" {
		return 0, nil
	}
	decimal := true
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			decimal = false
			break
		}
	}
	if decimal {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return 0, fmt.Errorf("Retry-After exceeds the maximum supported delay")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0, nil
	}
	// Time.Sub saturates on overflow, which would schedule before the requested date.
	if date.After(now.Add(time.Duration(math.MaxInt64))) {
		return 0, fmt.Errorf("Retry-After exceeds the maximum supported delay")
	}
	return date.Sub(now), nil
}
