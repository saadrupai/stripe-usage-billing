package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the production Store. Idempotency is enforced by primary keys,
// so concurrent retries of the same usage event or webhook are safe.
type Postgres struct{ db *pgxpool.Pool }

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

func (p *Postgres) CreateCustomer(ctx context.Context, c Customer) error {
	_, err := p.db.Exec(ctx, `
		INSERT INTO customers (id, email, stripe_customer_id, subscription_status, current_period_end)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5)`,
		c.ID, c.Email, c.StripeCustomerID, c.SubscriptionStatus, nullTime(c.CurrentPeriodEnd))
	return err
}

func (p *Postgres) GetCustomer(ctx context.Context, id string) (Customer, error) {
	var c Customer
	var sid *string
	var end *time.Time
	err := p.db.QueryRow(ctx, `
		SELECT id, email, stripe_customer_id, subscription_status, current_period_end, created_at
		FROM customers WHERE id = $1`, id).
		Scan(&c.ID, &c.Email, &sid, &c.SubscriptionStatus, &end, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if sid != nil {
		c.StripeCustomerID = *sid
	}
	if end != nil {
		c.CurrentPeriodEnd = *end
	}
	return c, err
}

func (p *Postgres) UpdateSubscriptionByStripeID(ctx context.Context, sid string, st SubscriptionStatus, end time.Time) error {
	tag, err := p.db.Exec(ctx, `
		UPDATE customers
		SET subscription_status = $2,
		    current_period_end = COALESCE($3, current_period_end)
		WHERE stripe_customer_id = $1`, sid, st, nullTime(end))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) InsertUsage(ctx context.Context, e UsageEvent) (bool, error) {
	tag, err := p.db.Exec(ctx, `
		INSERT INTO usage_events (id, customer_id, metric, quantity, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.CustomerID, e.Metric, e.Quantity, e.OccurredAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) UnreportedAggregates(ctx context.Context, limit int) ([]Aggregate, error) {
	rows, err := p.db.Query(ctx, `
		SELECT u.customer_id, c.stripe_customer_id, u.metric, SUM(u.quantity), array_agg(u.id ORDER BY u.id)
		FROM usage_events u
		JOIN customers c ON c.id = u.customer_id
		WHERE u.reported_at IS NULL AND c.stripe_customer_id IS NOT NULL
		GROUP BY u.customer_id, c.stripe_customer_id, u.metric
		ORDER BY u.customer_id, u.metric
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Aggregate
	for rows.Next() {
		var a Aggregate
		if err := rows.Scan(&a.CustomerID, &a.StripeCustomerID, &a.Metric, &a.Total, &a.EventIDs); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Postgres) MarkReported(ctx context.Context, ids []string, at time.Time) error {
	_, err := p.db.Exec(ctx, `UPDATE usage_events SET reported_at = $2 WHERE id = ANY($1)`, ids, at)
	return err
}

func (p *Postgres) UsageSince(ctx context.Context, customerID string, since time.Time) (map[string]int64, error) {
	rows, err := p.db.Query(ctx, `
		SELECT metric, SUM(quantity) FROM usage_events
		WHERE customer_id = $1 AND occurred_at >= $2
		GROUP BY metric`, customerID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var m string
		var n int64
		if err := rows.Scan(&m, &n); err != nil {
			return nil, err
		}
		out[m] = n
	}
	return out, rows.Err()
}

func (p *Postgres) MarkWebhookProcessed(ctx context.Context, eventID string) (bool, error) {
	tag, err := p.db.Exec(ctx, `INSERT INTO processed_webhooks (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
