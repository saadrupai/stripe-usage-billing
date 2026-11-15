package billing

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
	"github.com/saadrupai/stripe-usage-billing/internal/stripex"
)

type fakeStripe struct {
	sent []stripex.MeterEvent
	err  error
}

func (f *fakeStripe) SendMeterEvent(_ context.Context, ev stripex.MeterEvent) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, ev)
	return nil
}

func setup(t *testing.T) (*store.Memory, *fakeStripe, *Reporter) {
	t.Helper()
	st := store.NewMemory()
	ctx := context.Background()
	_ = st.CreateCustomer(ctx, store.Customer{ID: "c1", StripeCustomerID: "cus_1", SubscriptionStatus: store.StatusActive})
	for i, q := range []int64{3, 4, 5} {
		_, _ = st.InsertUsage(ctx, store.UsageEvent{ID: string(rune('a' + i)), CustomerID: "c1", Metric: "api_requests", Quantity: q, OccurredAt: time.Now()})
	}
	fs := &fakeStripe{}
	r := &Reporter{Store: st, Stripe: fs, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return st, fs, r
}

func TestFlushAggregatesAndMarksReported(t *testing.T) {
	_, fs, r := setup(t)

	n, err := r.Flush(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	if got := fs.sent[0].Value; got != 12 {
		t.Fatalf("aggregated value = %d, want 12", got)
	}

	// Second flush must send nothing: events are marked reported.
	if n, _ := r.Flush(context.Background()); n != 0 {
		t.Fatalf("second flush sent %d batches, want 0", n)
	}
}

func TestTransientErrorKeepsUsageForRetry(t *testing.T) {
	_, fs, r := setup(t)
	fs.err = &stripex.APIError{Status: 503}

	if _, err := r.Flush(context.Background()); err == nil {
		t.Fatal("expected error on 503")
	}
	fs.err = nil
	n, err := r.Flush(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("retry flush: n=%d err=%v", n, err)
	}
}

func TestBatchIDIsDeterministic(t *testing.T) {
	if batchID([]string{"a", "b"}) != batchID([]string{"a", "b"}) {
		t.Fatal("batch id must be stable so Stripe can de-duplicate retries")
	}
	if batchID([]string{"a", "b"}) == batchID([]string{"a", "c"}) {
		t.Fatal("different batches must have different ids")
	}
}
