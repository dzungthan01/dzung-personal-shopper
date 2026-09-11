package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

const alertColumns = `id, item_id, kind, dedupe_key, payload_json, created_at, notified_at, read_at`

// stampableColumns are the only columns stampAlerts may set. Column names cannot
// be bound as parameters, so this guard is what keeps its Sprintf injection-safe.
var stampableColumns = map[string]bool{"read_at": true, "notified_at": true}

// AddAlert stores an alert unless its dedupe key already exists. A duplicate is
// the mechanism working, so it reports created=false rather than an error.
func (s *Store) AddAlert(ctx context.Context, alert *model.Alert) (int64, bool, error) {
	if alert.CreatedAt.IsZero() {
		alert.CreatedAt = time.Now().UTC()
	}
	if alert.DedupeKey == "" {
		return 0, false, errors.New("alert has no dedupe key")
	}

	payloadJSON, err := json.Marshal(alert.Payload)
	if err != nil {
		return 0, false, fmt.Errorf("encode alert payload: %w", err)
	}

	// DO NOTHING makes RETURNING yield no row on conflict, which is how a
	// duplicate is detected without a second query.
	const query = `
		INSERT INTO alerts (item_id, kind, dedupe_key, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO NOTHING
		RETURNING id`

	var id int64
	err = s.db.QueryRowContext(ctx, query,
		alert.ItemID, string(alert.Kind), alert.DedupeKey,
		string(payloadJSON), formatTime(alert.CreatedAt),
	).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("add alert %q: %w", alert.DedupeKey, err)
	}

	alert.ID = id
	return id, true, nil
}

// ListAlerts returns alerts newest first. A limit of 0 means no limit.
func (s *Store) ListAlerts(ctx context.Context, unreadOnly bool, limit int) ([]model.Alert, error) {
	query := `SELECT ` + alertColumns + ` FROM alerts`
	if unreadOnly {
		query += ` WHERE read_at IS NULL`
	}
	query += ` ORDER BY created_at DESC`

	var arguments []any
	if limit > 0 {
		query += ` LIMIT ?`
		arguments = append(arguments, limit)
	}
	return s.queryAlerts(ctx, query, arguments...)
}

// PendingNotifications returns alerts whose push has not gone out yet, oldest
// first. Served by the partial index, so it stays cheap as delivered alerts pile up.
func (s *Store) PendingNotifications(ctx context.Context, limit int) ([]model.Alert, error) {
	query := `SELECT ` + alertColumns + ` FROM alerts WHERE notified_at IS NULL ORDER BY created_at`

	var arguments []any
	if limit > 0 {
		query += ` LIMIT ?`
		arguments = append(arguments, limit)
	}
	return s.queryAlerts(ctx, query, arguments...)
}

// AckAlerts marks alerts read and reports how many changed. Already-read ids
// are skipped, so the count can be lower than len(ids).
func (s *Store) AckAlerts(ctx context.Context, ids []int64) (int, error) {
	return s.stampAlerts(ctx, "read_at", ids, true)
}

// MarkNotified records that the push for these alerts has gone out.
func (s *Store) MarkNotified(ctx context.Context, ids []int64) error {
	_, err := s.stampAlerts(ctx, "notified_at", ids, false)
	return err
}

// stampAlerts sets a timestamp column on the given ids. When onlyUnset is true
// it skips rows already stamped, making the call idempotent.
func (s *Store) stampAlerts(ctx context.Context, column string, ids []int64, onlyUnset bool) (int, error) {
	if !stampableColumns[column] {
		return 0, fmt.Errorf("refusing to stamp unknown column %q", column)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(`UPDATE alerts SET %s = ? WHERE id IN (%s)`, column, placeholders)
	if onlyUnset {
		query += fmt.Sprintf(` AND %s IS NULL`, column)
	}

	arguments := make([]any, 0, len(ids)+1)
	arguments = append(arguments, formatTime(time.Now().UTC()))
	for _, id := range ids {
		arguments = append(arguments, id)
	}

	result, err := s.db.ExecContext(ctx, query, arguments...)
	if err != nil {
		return 0, fmt.Errorf("set alerts.%s: %w", column, err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (s *Store) queryAlerts(ctx context.Context, query string, arguments ...any) ([]model.Alert, error) {
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query alerts: %w", err)
	}
	defer rows.Close()

	var alerts []model.Alert
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, *alert)
	}
	return alerts, rows.Err()
}

func scanAlert(source scanner) (*model.Alert, error) {
	var alert model.Alert
	var payloadJSON, notifiedAt, readAt sql.NullString
	var createdAt string

	if err := source.Scan(&alert.ID, &alert.ItemID, &alert.Kind, &alert.DedupeKey,
		&payloadJSON, &createdAt, &notifiedAt, &readAt); err != nil {
		return nil, err
	}

	if payloadJSON.Valid && payloadJSON.String != "" {
		if err := json.Unmarshal([]byte(payloadJSON.String), &alert.Payload); err != nil {
			return nil, fmt.Errorf("alert %d payload: %w", alert.ID, err)
		}
	}

	var err error
	if alert.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("alert %d created_at: %w", alert.ID, err)
	}
	if alert.NotifiedAt, err = parseNullTime(notifiedAt); err != nil {
		return nil, fmt.Errorf("alert %d notified_at: %w", alert.ID, err)
	}
	if alert.ReadAt, err = parseNullTime(readAt); err != nil {
		return nil, fmt.Errorf("alert %d read_at: %w", alert.ID, err)
	}
	return &alert, nil
}
