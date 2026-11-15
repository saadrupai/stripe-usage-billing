package store

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-memory Store used for tests and local demos without Postgres.
type Memory struct {
	mu        sync.Mutex
	customers map[string]Customer
	byStripe  map[string]string
	usage     map[string]UsageEvent
	reported  map[string]time.Time
	webhooks  map[string]struct{}
}

func NewMemory() *Memory {
	return &Memory{
		customers: map[string]Customer{},
		byStripe:  map[string]string{},
		usage:     map[string]UsageEvent{},
		reported:  map[string]time.Time{},
		webhooks:  map[string]struct{}{},
	}
}

func (m *Memory) CreateCustomer(_ context.Context, c Customer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.customers[c.ID] = c
	if c.StripeCustomerID != "" {
		m.byStripe[c.StripeCustomerID] = c.ID
	}
	return nil
}

func (m *Memory) GetCustomer(_ context.Context, id string) (Customer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.customers[id]
	if !ok {
		return Customer{}, ErrNotFound
	}
	return c, nil
}

func (m *Memory) UpdateSubscriptionByStripeID(_ context.Context, sid string, st SubscriptionStatus, end time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byStripe[sid]
	if !ok {
		return ErrNotFound
	}
	c := m.customers[id]
	c.SubscriptionStatus = st
	if !end.IsZero() {
		c.CurrentPeriodEnd = end
	}
	m.customers[id] = c
	return nil
}

func (m *Memory) InsertUsage(_ context.Context, e UsageEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.usage[e.ID]; dup {
		return false, nil
	}
	m.usage[e.ID] = e
	return true, nil
}

func (m *Memory) UnreportedAggregates(_ context.Context, limit int) ([]Aggregate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type key struct{ cust, metric string }
	agg := map[key]*Aggregate{}
	ids := make([]string, 0, len(m.usage))
	for id := range m.usage {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic output
	for _, id := range ids {
		if _, done := m.reported[id]; done {
			continue
		}
		e := m.usage[id]
		k := key{e.CustomerID, e.Metric}
		a, ok := agg[k]
		if !ok {
			a = &Aggregate{CustomerID: e.CustomerID, StripeCustomerID: m.customers[e.CustomerID].StripeCustomerID, Metric: e.Metric}
			agg[k] = a
		}
		a.Total += e.Quantity
		a.EventIDs = append(a.EventIDs, id)
	}
	out := make([]Aggregate, 0, len(agg))
	for _, a := range agg {
		if a.StripeCustomerID == "" {
			continue // not linked to Stripe yet; nothing to report
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CustomerID+out[i].Metric < out[j].CustomerID+out[j].Metric })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) MarkReported(_ context.Context, ids []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		m.reported[id] = at
	}
	return nil
}

func (m *Memory) UsageSince(_ context.Context, customerID string, since time.Time) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for _, e := range m.usage {
		if e.CustomerID == customerID && !e.OccurredAt.Before(since) {
			out[e.Metric] += e.Quantity
		}
	}
	return out, nil
}

func (m *Memory) MarkWebhookProcessed(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.webhooks[eventID]; seen {
		return false, nil
	}
	m.webhooks[eventID] = struct{}{}
	return true, nil
}
