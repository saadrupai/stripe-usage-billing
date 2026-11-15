package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saadrupai/stripe-usage-billing/internal/store"
	"github.com/saadrupai/stripe-usage-billing/internal/stripex"
)

const secret = "whsec_test"

func newHandler(t *testing.T) (*Handler, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	_ = st.CreateCustomer(context.Background(), store.Customer{ID: "c1", StripeCustomerID: "cus_1", SubscriptionStatus: store.StatusActive})
	return &Handler{Store: st, Secret: secret, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st
}

func post(h http.Handler, body string, sign bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	if sign {
		req.Header.Set("Stripe-Signature", stripex.SignHeader(time.Now().Unix(), []byte(body), secret))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func status(t *testing.T, st *store.Memory) store.SubscriptionStatus {
	c, err := st.GetCustomer(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	return c.SubscriptionStatus
}

func TestRejectsUnsignedRequests(t *testing.T) {
	h, _ := newHandler(t)
	if rec := post(h, `{"id":"evt_1","type":"invoice.paid"}`, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestPaymentFailedThenPaid(t *testing.T) {
	h, st := newHandler(t)

	post(h, `{"id":"evt_1","type":"invoice.payment_failed","data":{"object":{"customer":"cus_1"}}}`, true)
	if s := status(t, st); s != store.StatusPastDue {
		t.Fatalf("after failure: %s", s)
	}
	if !status(t, st).HasAccess() {
		t.Fatal("past_due should keep access during the grace period")
	}

	post(h, `{"id":"evt_2","type":"invoice.paid","data":{"object":{"customer":"cus_1"}}}`, true)
	if s := status(t, st); s != store.StatusActive {
		t.Fatalf("after payment: %s", s)
	}
}

func TestSubscriptionDeletedRevokesAccess(t *testing.T) {
	h, st := newHandler(t)
	post(h, `{"id":"evt_3","type":"customer.subscription.deleted","data":{"object":{"customer":"cus_1","status":"canceled"}}}`, true)
	if status(t, st).HasAccess() {
		t.Fatal("canceled customer must lose access")
	}
}

func TestDuplicateDeliveryIsProcessedOnce(t *testing.T) {
	h, st := newHandler(t)
	failed := `{"id":"evt_dup","type":"invoice.payment_failed","data":{"object":{"customer":"cus_1"}}}`
	post(h, failed, true)
	post(h, `{"id":"evt_paid","type":"invoice.paid","data":{"object":{"customer":"cus_1"}}}`, true)

	// A late duplicate of the old failure must not flip the customer back to past_due.
	if rec := post(h, failed, true); rec.Code != http.StatusOK {
		t.Fatalf("duplicate should be acknowledged, got %d", rec.Code)
	}
	if s := status(t, st); s != store.StatusActive {
		t.Fatalf("duplicate event changed state to %s", s)
	}
}
