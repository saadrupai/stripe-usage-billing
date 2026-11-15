// Package store defines persistence for customers, usage events and processed webhooks.
package store

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("store: not found")

// SubscriptionStatus mirrors Stripe's subscription statuses that matter for access control.
type SubscriptionStatus string

const (
	StatusTrialing SubscriptionStatus = "trialing"
	StatusActive   SubscriptionStatus = "active"
	StatusPastDue  SubscriptionStatus = "past_due"
	StatusCanceled SubscriptionStatus = "canceled"
	StatusUnpaid   SubscriptionStatus = "unpaid"
)

// HasAccess reports whether a customer in this status may keep consuming the product.
// past_due keeps access during Stripe's smart-retry window (a grace period).
func (s SubscriptionStatus) HasAccess() bool {
	switch s {
	case StatusActive, StatusTrialing, StatusPastDue:
		return true
	}
	return false
}

type Customer struct {
	ID                 string
	Email              string
	StripeCustomerID   string
	SubscriptionStatus SubscriptionStatus
	CurrentPeriodEnd   time.Time
	CreatedAt          time.Time
}

type UsageEvent struct {
	ID         string // client-supplied idempotency key
	CustomerID string
	Metric     string
	Quantity   int64
	OccurredAt time.Time
}

// Aggregate is unreported usage for one customer+metric, ready to send to Stripe.
type Aggregate struct {
	CustomerID       string
	StripeCustomerID string
	Metric           string
	Total            int64
	EventIDs         []string
}

type Store interface {
	CreateCustomer(ctx context.Context, c Customer) error
	GetCustomer(ctx context.Context, id string) (Customer, error)
	UpdateSubscriptionByStripeID(ctx context.Context, stripeCustomerID string, status SubscriptionStatus, periodEnd time.Time) error

	// InsertUsage stores an event; inserted is false if the ID was already seen (idempotent retry).
	InsertUsage(ctx context.Context, e UsageEvent) (inserted bool, err error)
	UnreportedAggregates(ctx context.Context, limit int) ([]Aggregate, error)
	MarkReported(ctx context.Context, eventIDs []string, at time.Time) error
	UsageSince(ctx context.Context, customerID string, since time.Time) (map[string]int64, error)

	// MarkWebhookProcessed returns false if this Stripe event ID was already handled.
	MarkWebhookProcessed(ctx context.Context, eventID string) (first bool, err error)
}
