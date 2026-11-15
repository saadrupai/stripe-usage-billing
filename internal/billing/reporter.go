// Package billing pushes aggregated usage to Stripe Billing Meters.
package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
	"github.com/saadrupai/stripe-usage-billing/internal/stripex"
)

// MeterSender is the subset of the Stripe client the reporter needs (mockable in tests).
type MeterSender interface {
	SendMeterEvent(ctx context.Context, ev stripex.MeterEvent) error
}

// Reporter periodically aggregates unreported usage and sends it to Stripe.
//
// Design notes:
//   - Usage is written locally first (fast, durable) and reported asynchronously,
//     so a Stripe outage never blocks the product.
//   - Each batch gets a deterministic identifier (hash of its event IDs). If we crash
//     after Stripe accepted the batch but before marking it reported, the retry sends
//     the same identifier and Stripe de-duplicates it: no double billing.
type Reporter struct {
	Store     store.Store
	Stripe    MeterSender
	Interval  time.Duration
	BatchSize int
	Log       *slog.Logger
	Now       func() time.Time
}

func (r *Reporter) Run(ctx context.Context) {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		if _, err := r.Flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
			r.Log.Error("usage flush failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Flush reports one round of aggregates and returns how many were sent.
func (r *Reporter) Flush(ctx context.Context) (int, error) {
	aggs, err := r.Store.UnreportedAggregates(ctx, r.batchSize())
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, a := range aggs {
		ev := stripex.MeterEvent{
			EventName:        a.Metric,
			StripeCustomerID: a.StripeCustomerID,
			Value:            a.Total,
			Identifier:       batchID(a.EventIDs),
			Timestamp:        r.now(),
		}
		if err := r.Stripe.SendMeterEvent(ctx, ev); err != nil {
			var apiErr *stripex.APIError
			if errors.As(err, &apiErr) && !apiErr.Retryable() {
				r.Log.Error("stripe rejected usage; leaving unreported for investigation",
					"customer", a.CustomerID, "metric", a.Metric, "err", err)
				continue
			}
			return sent, err // transient: try again next tick
		}
		if err := r.Store.MarkReported(ctx, a.EventIDs, r.now()); err != nil {
			return sent, err
		}
		sent++
		r.Log.Info("usage reported", "customer", a.CustomerID, "metric", a.Metric, "value", a.Total, "events", len(a.EventIDs))
	}
	return sent, nil
}

func (r *Reporter) batchSize() int {
	if r.BatchSize > 0 {
		return r.BatchSize
	}
	return 100
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func batchID(ids []string) string {
	h := sha256.Sum256([]byte(strings.Join(ids, "|")))
	return "usage_" + hex.EncodeToString(h[:16])
}
