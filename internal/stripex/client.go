package stripex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal Stripe REST client covering what this service needs:
// reporting usage to Stripe Billing Meters.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: "https://api.stripe.com",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// WithBaseURL points the client at a different host (e.g. stripe-mock or a test server).
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = strings.TrimRight(u, "/")
	return c
}

// MeterEvent is one usage report sent to a Stripe Billing Meter.
type MeterEvent struct {
	EventName        string // the meter's event_name, e.g. "api_requests"
	StripeCustomerID string // cus_...
	Value            int64
	Identifier       string // unique id so Stripe de-duplicates retries
	Timestamp        time.Time
}

// SendMeterEvent reports usage via POST /v1/billing/meter_events.
// Stripe de-duplicates on Identifier, which makes retries safe.
func (c *Client) SendMeterEvent(ctx context.Context, ev MeterEvent) error {
	form := url.Values{}
	form.Set("event_name", ev.EventName)
	form.Set("payload[stripe_customer_id]", ev.StripeCustomerID)
	form.Set("payload[value]", strconv.FormatInt(ev.Value, 10))
	form.Set("identifier", ev.Identifier)
	form.Set("timestamp", strconv.FormatInt(ev.Timestamp.Unix(), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/billing/meter_events", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.apiKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", ev.Identifier)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("stripe: send meter event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var se struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &se)
		return &APIError{Status: resp.StatusCode, Code: se.Error.Code, Message: se.Error.Message}
	}
	return nil
}

// APIError is returned for non-2xx responses from Stripe.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("stripe: %d %s: %s", e.Status, e.Code, e.Message)
}

// Retryable reports whether the request may succeed if retried.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}
