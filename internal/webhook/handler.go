// Package webhook handles Stripe webhook events: verified, idempotent, and
// fast to acknowledge.
package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
	"github.com/saadrupai/stripe-usage-billing/internal/stripex"
)

const maxBody = 1 << 16 // Stripe payloads are small; refuse anything huge

type Handler struct {
	Store  store.Store
	Secret string
	Log    *slog.Logger
	Now    func() time.Time
}

type event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type subscriptionObj struct {
	Customer         string `json:"customer"`
	Status           string `json:"status"`
	CurrentPeriodEnd int64  `json:"current_period_end"`
}

type invoiceObj struct {
	Customer string `json:"customer"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	// 1. Authenticate: reject anything not signed by Stripe.
	if err := stripex.VerifySignature(body, r.Header.Get("Stripe-Signature"), h.Secret, stripex.DefaultTolerance, h.now()); err != nil {
		h.Log.Warn("webhook signature rejected", "err", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var ev event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	// 2. Idempotency: Stripe delivers at-least-once. Process each event ID exactly once.
	first, err := h.Store.MarkWebhookProcessed(r.Context(), ev.ID)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError) // 5xx -> Stripe retries
		return
	}
	if !first {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 3. Handle the event types that change access.
	if err := h.handle(r, ev); err != nil {
		h.Log.Error("webhook handling failed", "event", ev.ID, "type", ev.Type, "err", err)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handle(r *http.Request, ev event) error {
	ctx := r.Context()
	switch ev.Type {
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		var s subscriptionObj
		if err := json.Unmarshal(ev.Data.Object, &s); err != nil {
			return err
		}
		status := store.SubscriptionStatus(s.Status)
		if ev.Type == "customer.subscription.deleted" {
			status = store.StatusCanceled
		}
		var end time.Time
		if s.CurrentPeriodEnd > 0 {
			end = time.Unix(s.CurrentPeriodEnd, 0)
		}
		return ignoreNotFound(h.Store.UpdateSubscriptionByStripeID(ctx, s.Customer, status, end))

	case "invoice.payment_failed":
		// Keep access during Stripe's retry window but flag the account.
		var inv invoiceObj
		if err := json.Unmarshal(ev.Data.Object, &inv); err != nil {
			return err
		}
		return ignoreNotFound(h.Store.UpdateSubscriptionByStripeID(ctx, inv.Customer, store.StatusPastDue, time.Time{}))

	case "invoice.paid":
		var inv invoiceObj
		if err := json.Unmarshal(ev.Data.Object, &inv); err != nil {
			return err
		}
		return ignoreNotFound(h.Store.UpdateSubscriptionByStripeID(ctx, inv.Customer, store.StatusActive, time.Time{}))
	}
	return nil // unhandled types are acknowledged and ignored
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func ignoreNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil // event for a customer this service doesn't manage
	}
	return err
}
