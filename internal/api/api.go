// Package api exposes the HTTP API: customers, usage ingestion and usage summaries.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
)

type Server struct {
	Store  store.Store
	APIKey string
	Log    *slog.Logger
}

// Routes wires the API. Uses Go 1.22+ method/path patterns from net/http.
func (s *Server) Routes(webhook http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("POST /webhooks/stripe", webhook) // authenticated by Stripe signature, not API key
	mux.Handle("POST /v1/customers", s.auth(http.HandlerFunc(s.createCustomer)))
	mux.Handle("POST /v1/usage", s.auth(http.HandlerFunc(s.recordUsage)))
	mux.Handle("GET /v1/customers/{id}/usage", s.auth(http.HandlerFunc(s.usageSummary)))
	return s.logRequests(mux)
}

type createCustomerReq struct {
	ID               string `json:"id"`
	Email            string `json:"email"`
	StripeCustomerID string `json:"stripe_customer_id"`
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var req createCustomerReq
	if err := decode(r, &req); err != nil || req.ID == "" || req.Email == "" {
		writeErr(w, http.StatusBadRequest, "id and email are required")
		return
	}
	c := store.Customer{ID: req.ID, Email: req.Email, StripeCustomerID: req.StripeCustomerID, SubscriptionStatus: store.StatusTrialing}
	if err := s.Store.CreateCustomer(r.Context(), c); err != nil {
		writeErr(w, http.StatusConflict, "could not create customer")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": c.ID, "subscription_status": c.SubscriptionStatus})
}

type usageReq struct {
	CustomerID string    `json:"customer_id"`
	Metric     string    `json:"metric"`
	Quantity   int64     `json:"quantity"`
	OccurredAt time.Time `json:"occurred_at"`
}

// recordUsage ingests one usage event. Clients must send an Idempotency-Key header;
// retries with the same key are accepted but counted once.
func (s *Server) recordUsage(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeErr(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}
	var req usageReq
	if err := decode(r, &req); err != nil || req.CustomerID == "" || req.Metric == "" || req.Quantity <= 0 {
		writeErr(w, http.StatusBadRequest, "customer_id, metric and a positive quantity are required")
		return
	}
	if req.OccurredAt.IsZero() {
		req.OccurredAt = time.Now()
	}

	c, err := s.Store.GetCustomer(r.Context(), req.CustomerID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown customer")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	// Entitlement check: canceled/unpaid customers can't keep consuming.
	if !c.SubscriptionStatus.HasAccess() {
		writeErr(w, http.StatusPaymentRequired, "subscription inactive: "+string(c.SubscriptionStatus))
		return
	}

	inserted, err := s.Store.InsertUsage(r.Context(), store.UsageEvent{
		ID: key, CustomerID: req.CustomerID, Metric: req.Metric, Quantity: req.Quantity, OccurredAt: req.OccurredAt,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	code := http.StatusAccepted
	if !inserted {
		code = http.StatusOK // duplicate retry, already counted
	}
	writeJSON(w, code, map[string]any{"id": key, "duplicate": !inserted})
}

func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := s.Store.GetCustomer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown customer")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	since := time.Now().AddDate(0, -1, 0)
	if !c.CurrentPeriodEnd.IsZero() {
		since = c.CurrentPeriodEnd.AddDate(0, -1, 0) // start of the current monthly period
	}
	usage, err := s.Store.UsageSince(r.Context(), id, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer_id":         id,
		"subscription_status": c.SubscriptionStatus,
		"period_start":        since,
		"usage":               usage,
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.APIKey)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
