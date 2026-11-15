package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
)

func newServer(t *testing.T) (http.Handler, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	_ = st.CreateCustomer(context.Background(), store.Customer{ID: "c1", Email: "a@b.co", StripeCustomerID: "cus_1", SubscriptionStatus: store.StatusActive})
	_ = st.CreateCustomer(context.Background(), store.Customer{ID: "c2", Email: "x@y.co", StripeCustomerID: "cus_2", SubscriptionStatus: store.StatusCanceled})
	s := &Server{Store: st, APIKey: "key", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.Routes(http.NotFoundHandler()), st
}

func do(h http.Handler, method, path, body, idem string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer key")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUsageIsIdempotent(t *testing.T) {
	h, st := newServer(t)
	body := `{"customer_id":"c1","metric":"api_requests","quantity":10}`

	if rec := do(h, "POST", "/v1/usage", body, "req-1"); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/v1/usage", body, "req-1"); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d", rec.Code)
	}
	aggs, _ := st.UnreportedAggregates(context.Background(), 10)
	if len(aggs) != 1 || aggs[0].Total != 10 {
		t.Fatalf("usage double counted: %+v", aggs)
	}
}

func TestCanceledCustomerIsBlocked(t *testing.T) {
	h, _ := newServer(t)
	rec := do(h, "POST", "/v1/usage", `{"customer_id":"c2","metric":"api_requests","quantity":1}`, "req-2")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("code = %d, want 402", rec.Code)
	}
}

func TestRequiresAPIKeyAndIdempotencyKey(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("POST", "/v1/usage", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := do(h, "POST", "/v1/usage", `{"customer_id":"c1","metric":"m","quantity":1}`, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no idempotency key: %d", rec.Code)
	}
}
