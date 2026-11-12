package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"epay-bridge/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrOrderConflict = errors.New("merchant order number already exists with different immutable fields")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	// CREATE TABLE IF NOT EXISTS is not enough to make concurrent first-start
	// migrations safe: PostgreSQL can still race while inserting catalog rows.
	// A session-scoped advisory lock serializes migrations across processes and
	// is automatically released if the connection is lost.
	const migrationLockID int64 = 0x4550415942524944 // "EPAYBRID"
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) CreateOrGetOrder(ctx context.Context, in domain.CreateOrder) (*domain.Order, bool, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, false, err
	}
	token, err := randomHex(24)
	if err != nil {
		return nil, false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO orders (
			id, checkout_token, merchant_order_no, channel, name, amount_cents,
			status, notify_url, return_url, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,'pending',$7,$8,$9)
		ON CONFLICT (merchant_order_no) DO NOTHING`,
		id, token, in.MerchantOrderNo, in.Channel, in.Name, in.AmountCents,
		in.NotifyURL, in.ReturnURL, in.ExpiresAt,
	)
	if err != nil {
		return nil, false, err
	}
	order, err := s.GetOrderByMerchantNo(ctx, in.MerchantOrderNo)
	if err != nil {
		return nil, false, err
	}
	created := tag.RowsAffected() == 1
	if !created && (order.Channel != in.Channel || order.Name != in.Name || order.AmountCents != in.AmountCents || order.NotifyURL != in.NotifyURL || order.ReturnURL != in.ReturnURL) {
		return nil, false, ErrOrderConflict
	}
	return order, created, nil
}

func (s *Store) SetProviderPayURL(ctx context.Context, orderID, payURL string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE orders SET provider_pay_url=$2, updated_at=now()
		WHERE id=$1 AND status='pending' AND provider_pay_url=''`, orderID, payURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		order, getErr := s.GetOrderByID(ctx, orderID)
		if getErr != nil {
			return getErr
		}
		if order.ProviderPayURL == "" {
			return errors.New("order is no longer payable")
		}
	}
	return nil
}

func (s *Store) GetOrderByID(ctx context.Context, id string) (*domain.Order, error) {
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE id=$1`, id))
}

func (s *Store) GetOrderByToken(ctx context.Context, token string) (*domain.Order, error) {
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE checkout_token=$1`, token))
}

func (s *Store) GetOrderByMerchantNo(ctx context.Context, merchantOrderNo string) (*domain.Order, error) {
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE merchant_order_no=$1`, merchantOrderNo))
}

func (s *Store) MarkPaid(ctx context.Context, confirmation domain.PaymentConfirmation) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := scanOrder(tx.QueryRow(ctx, orderSelect+` WHERE id=$1 FOR UPDATE`, confirmation.OrderID))
	if err != nil {
		return false, err
	}
	if order.Status == domain.StatusPaid {
		if order.ProviderTransactionID != confirmation.ProviderTransactionID {
			return false, errors.New("paid order has a different provider transaction")
		}
		return false, tx.Commit(ctx)
	}
	if order.Status != domain.StatusPending {
		return false, fmt.Errorf("cannot pay order in %s state", order.Status)
	}
	paidAt := confirmation.PaidAt
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='paid', provider_transaction_id=$2,
		provider_payload=$3, paid_at=$4, updated_at=now() WHERE id=$1`,
		confirmation.OrderID, confirmation.ProviderTransactionID, confirmation.ProviderPayload, paidAt); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notify_deliveries (order_id, notify_url)
		VALUES ($1,$2) ON CONFLICT (order_id) DO NOTHING`, order.ID, order.NotifyURL); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *Store) DueDeliveries(ctx context.Context, limit int) ([]domain.Delivery, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, order_id, notify_url, attempts, next_attempt_at
		FROM notify_deliveries WHERE delivered_at IS NULL AND next_attempt_at <= now()
		ORDER BY next_attempt_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Delivery
	for rows.Next() {
		var d domain.Delivery
		if err := rows.Scan(&d.ID, &d.OrderID, &d.NotifyURL, &d.Attempts, &d.NextAttempt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) MarkDeliverySucceeded(ctx context.Context, id int64, status int) error {
	_, err := s.pool.Exec(ctx, `UPDATE notify_deliveries SET delivered_at=now(), attempts=attempts+1,
		last_status=$2, last_error='', updated_at=now() WHERE id=$1`, id, status)
	return err
}

func (s *Store) MarkDeliveryFailed(ctx context.Context, id int64, status int, message string, next time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE notify_deliveries SET attempts=attempts+1,
		last_status=$2, last_error=$3, next_attempt_at=$4, updated_at=now() WHERE id=$1`,
		id, status, message, next)
	return err
}

func (s *Store) PendingOrders(ctx context.Context, olderThan time.Time, limit int) ([]domain.Order, error) {
	rows, err := s.pool.Query(ctx, orderSelect+` WHERE status='pending' AND created_at <= $1 ORDER BY created_at LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []domain.Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, *order)
	}
	return orders, rows.Err()
}

func (s *Store) MarkClosed(ctx context.Context, orderID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE orders SET status='closed', updated_at=now()
		WHERE id=$1 AND status='pending'`, orderID)
	return err
}

func (s *Store) RecentOrders(ctx context.Context, limit int) ([]domain.Order, error) {
	rows, err := s.pool.Query(ctx, orderSelect+` ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []domain.Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, *order)
	}
	return orders, rows.Err()
}

const orderSelect = `SELECT id, checkout_token, merchant_order_no, channel, name,
	amount_cents, status, notify_url, return_url, provider_transaction_id,
	provider_pay_url, provider_payload, created_at, updated_at, paid_at, expires_at FROM orders`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrder(row rowScanner) (*domain.Order, error) {
	var order domain.Order
	var status string
	err := row.Scan(
		&order.ID, &order.CheckoutToken, &order.MerchantOrderNo, &order.Channel,
		&order.Name, &order.AmountCents, &status, &order.NotifyURL,
		&order.ReturnURL, &order.ProviderTransactionID, &order.ProviderPayURL,
		&order.ProviderPayload, &order.CreatedAt, &order.UpdatedAt, &order.PaidAt,
		&order.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	order.Status = domain.OrderStatus(status)
	return &order, nil
}

func randomHex(bytesCount int) (string, error) {
	b := make([]byte, bytesCount)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
