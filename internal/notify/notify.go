// Package notify delivers donation notifications from a transactional outbox.
// Queue must be called in the same transaction that records the donation.
package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxAttempts = 8
	maxPayload  = 256 * 1024
)

type WebhookConfig struct {
	URL     string `json:"url"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`
	Enabled  bool   `json:"enabled"`
}

type Config struct {
	Webhook WebhookConfig `json:"webhook"`
	SMTP    SMTPConfig    `json:"smtp"`
}

type Event struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Donation  any    `json:"donation,omitempty"`
	Payment   any    `json:"payment,omitempty"`
}

// Job deliberately excludes the payload and configuration, which can contain
// donor information or credentials. LastError contains only safe error labels.
type Job struct {
	ID        string `json:"id"`
	EventID   string `json:"event_id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	CreatedAt string `json:"created_at"`
}

type Service struct {
	db      *sql.DB
	wake    chan struct{}
	now     func() time.Time
	running atomic.Bool
}

// New creates the outbox schema. The single Run worker reclaims interrupted
// deliveries before it starts; an acknowledgement lost during a restart can
// produce another delivery with the same delivery ID.
func New(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, errors.New("notification database is required")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS notification_jobs (
		id TEXT PRIMARY KEY,
		event_id TEXT NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('webhook', 'smtp')),
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'delivered', 'failed')),
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		next_attempt_at INTEGER NOT NULL,
		locked_at INTEGER NOT NULL DEFAULT 0,
		payload BLOB NOT NULL,
		config BLOB NOT NULL,
		UNIQUE (event_id, kind)
	);
	CREATE INDEX IF NOT EXISTS notification_jobs_due ON notification_jobs (status, next_attempt_at);`)
	if err != nil {
		return nil, fmt.Errorf("create notification outbox: %w", err)
	}
	return &Service{db: db, wake: make(chan struct{}, 1), now: time.Now}, nil
}

// Queue snapshots the enabled destination configurations inside the caller's
// transaction. Repeated enqueueing of an event ID never creates duplicate jobs.
func (s *Service) Queue(tx *sql.Tx, event Event, cfg Config) error {
	if tx == nil {
		return errors.New("notification transaction is required")
	}
	if !cfg.Webhook.Enabled && !cfg.SMTP.Enabled {
		return nil
	}
	// DNS and network availability must never block recording a donation.
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if event.ID == "" || len(event.ID) > 128 || strings.ContainsAny(event.ID, "\r\n") {
		return errors.New("notification event ID is invalid")
	}
	if event.Type == "" || len(event.Type) > 128 || strings.ContainsAny(event.Type, "\r\n") {
		return errors.New("notification event type is invalid")
	}
	now := s.now().UTC()
	if event.CreatedAt == "" {
		event.CreatedAt = now.Format(time.RFC3339Nano)
	}
	body, err := json.Marshal(event)
	if err != nil {
		return errors.New("notification event cannot be encoded")
	}
	if len(body) > maxPayload {
		return errors.New("notification event exceeds the size limit")
	}
	for _, destination := range []struct {
		kind    string
		enabled bool
		config  any
	}{{"webhook", cfg.Webhook.Enabled, cfg.Webhook}, {"smtp", cfg.SMTP.Enabled, cfg.SMTP}} {
		if !destination.enabled {
			continue
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		config, err := json.Marshal(destination.config)
		if err != nil {
			return errors.New("notification configuration cannot be encoded")
		}
		_, err = tx.Exec(`INSERT INTO notification_jobs
			(id, event_id, kind, created_at, next_attempt_at, payload, config)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (event_id, kind) DO NOTHING`,
			id, event.ID, destination.kind, now.Format(time.RFC3339Nano), now.Unix(), body, config)
		if err != nil {
			return fmt.Errorf("queue notification: %w", err)
		}
	}
	s.signal()
	return nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("notification delivery ID could not be generated")
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run runs one delivery worker until ctx is canceled. Database claims are
// committed before any network work, so one SQLite connection is sufficient.
func (s *Service) Run(ctx context.Context) {
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	defer s.running.Store(false)
	// A process restart abandons any old processing lease. Retries retain the
	// same delivery ID and count the interrupted attempt toward the limit.
	if _, err := s.db.ExecContext(ctx, `UPDATE notification_jobs SET
		status = CASE WHEN attempts >= ? THEN 'failed' ELSE 'pending' END,
		last_error = 'delivery interrupted', locked_at = 0, next_attempt_at = ?
		WHERE status = 'processing'`, maxAttempts, s.now().Unix()); err != nil {
		if ctx.Err() == nil {
			log.Print("notify: could not recover interrupted deliveries")
		}
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		didWork, err := s.deliverNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Print("notify: outbox database operation failed")
		}
		if didWork && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
			// A failed acknowledgement write can leave a lease behind even
			// without restarting the process. Reclaim it after the maximum
			// network timeout and database acknowledgement window have passed.
			if _, err := s.db.ExecContext(ctx, `UPDATE notification_jobs SET
				status = CASE WHEN attempts >= ? THEN 'failed' ELSE 'pending' END,
				last_error = 'delivery interrupted', locked_at = 0, next_attempt_at = ?
				WHERE status = 'processing' AND locked_at <= ?`, maxAttempts,
				s.now().Unix(), s.now().Add(-time.Minute).Unix()); err != nil && ctx.Err() == nil {
				log.Print("notify: could not recover expired delivery leases")
			}
		}
	}
}

type delivery struct {
	Job
	payload []byte
	config  []byte
}

func (s *Service) claim(ctx context.Context) (delivery, bool, error) {
	var d delivery
	// UPDATE ... RETURNING makes selecting and claiming a due job atomic.
	err := s.db.QueryRowContext(ctx, `UPDATE notification_jobs SET
		status = 'processing', attempts = attempts + 1, locked_at = ?
		WHERE id = (SELECT id FROM notification_jobs WHERE status = 'pending'
			AND next_attempt_at <= ? AND attempts < ? ORDER BY next_attempt_at, created_at, id LIMIT 1)
		AND status = 'pending'
		RETURNING id, event_id, kind, status, attempts, last_error, created_at, payload, config`,
		s.now().Unix(), s.now().Unix(), maxAttempts).Scan(&d.ID, &d.EventID, &d.Kind, &d.Status,
		&d.Attempts, &d.LastError, &d.CreatedAt, &d.payload, &d.config)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

func (s *Service) deliverNext(ctx context.Context) (bool, error) {
	d, found, err := s.claim(ctx)
	if err != nil || !found {
		return found, err
	}
	var deliveryErr error
	switch d.Kind {
	case "webhook":
		var cfg WebhookConfig
		if json.Unmarshal(d.config, &cfg) != nil {
			deliveryErr = errors.New("saved webhook configuration is invalid")
		} else {
			deliveryErr = sendWebhook(ctx, d.ID, d.payload, cfg)
		}
	case "smtp":
		var cfg SMTPConfig
		if json.Unmarshal(d.config, &cfg) != nil {
			deliveryErr = errors.New("saved SMTP configuration is invalid")
		} else {
			deliveryErr = sendSMTP(ctx, d.payload, cfg)
		}
	default:
		deliveryErr = errors.New("saved notification kind is invalid")
	}
	// Persist cancellation using a fresh bounded context. The process may still
	// die before this update, in which case startup recovery reclaims the job.
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if deliveryErr == nil {
		_, err = s.db.ExecContext(finishCtx, `UPDATE notification_jobs SET status = 'delivered',
			last_error = '', locked_at = 0 WHERE id = ? AND status = 'processing'`, d.ID)
	} else {
		status := "pending"
		if d.Attempts >= maxAttempts {
			status = "failed"
		}
		_, err = s.db.ExecContext(finishCtx, `UPDATE notification_jobs SET status = ?,
			last_error = ?, next_attempt_at = ?, locked_at = 0 WHERE id = ? AND status = 'processing'`,
			status, deliveryErr.Error(), s.now().Add(retryDelay(d.Attempts)).Unix(), d.ID)
	}
	return true, err
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxAttempts {
		attempt = maxAttempts
	}
	// 30s, 1m, 2m, 4m, 8m, 16m, 32m, 1h (bounded).
	delay := 30 * time.Second * time.Duration(1<<(attempt-1))
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (s *Service) List(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, kind, status, attempts,
		last_error, created_at FROM notification_jobs ORDER BY created_at DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.EventID, &job.Kind, &job.Status, &job.Attempts, &job.LastError, &job.CreatedAt); err != nil {
			return nil, fmt.Errorf("read notification delivery: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Retry requeues only a failed job. The delivery ID and snapshot are unchanged;
// resetting attempts gives the administrator another bounded set of attempts.
func (s *Service) Retry(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE notification_jobs SET status = 'pending',
		attempts = 0, last_error = '', next_attempt_at = ?, locked_at = 0
		WHERE id = ? AND status = 'failed'`, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("retry notification: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("retry notification: %w", err)
	}
	if changed == 0 {
		return errors.New("notification delivery is missing or is not failed")
	}
	s.signal()
	return nil
}
