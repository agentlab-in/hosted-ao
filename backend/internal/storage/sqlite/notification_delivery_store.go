package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/notification"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

const deliveryColumns = `id, notification_id, notification_seq, project_id, session_id,
    route_name, sink, destination_key, request_json,
    status, attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at,
    last_error_code, last_error, external_id,
    created_at, updated_at, delivered_at`

const (
	defaultDeliveryLimit       = 100
	defaultDeliveryMaxAttempts = 5 // mirrors notification_deliveries.max_attempts schema default
)

type DeliveryFilter struct {
	NotificationID string
	ProjectID      string
	Status         notification.DeliveryStatus
	Limit          int
}

func (s *Store) ListUnroutedNotifications(ctx context.Context, limit int) ([]domain.Notification, error) {
	if limit <= 0 {
		limit = defaultNotificationLimit
	}
	rows, err := s.qr.ListUnroutedNotifications(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list unrouted notifications: %w", err)
	}
	return notificationsFromGen(rows)
}

func (s *Store) MarkNotificationRouted(ctx context.Context, id domain.NotificationID, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.MarkNotificationRouted(ctx, gen.MarkNotificationRoutedParams{
		RoutedAt:  nullTime(at),
		UpdatedAt: at,
		ID:        string(id),
	}); err != nil {
		return fmt.Errorf("mark notification routed %s: %w", id, err)
	}
	return nil
}

func (s *Store) EnqueueDelivery(ctx context.Context, row notification.DeliveryRow) (notification.DeliveryRow, bool, error) {
	now := row.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row, err := notification.NormalizeDelivery(row, now, defaultDeliveryMaxAttempts)
	if err != nil {
		return notification.DeliveryRow{}, false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	got, err := s.qw.InsertNotificationDelivery(ctx, gen.InsertNotificationDeliveryParams{
		ID:              row.ID,
		NotificationID:  string(row.NotificationID),
		NotificationSeq: row.NotificationSeq,
		ProjectID:       string(row.ProjectID),
		SessionID:       string(row.SessionID),
		RouteName:       row.RouteName,
		Sink:            row.Sink,
		DestinationKey:  row.DestinationKey,
		RequestJson:     string(row.RequestJSON),
		Status:          string(row.Status),
		Attempts:        int64(row.Attempts),
		MaxAttempts:     int64(row.MaxAttempts),
		NextAttemptAt:   row.NextAttemptAt,
		LeaseOwner:      row.LeaseOwner,
		LeaseExpiresAt:  nullTime(row.LeaseExpiresAt),
		LastErrorCode:   row.LastErrorCode,
		LastError:       row.LastError,
		ExternalID:      row.ExternalID,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		DeliveredAt:     nullTime(row.DeliveredAt),
	})
	if errors.Is(err, sql.ErrNoRows) {
		existing, readErr := s.getDeliveryByUniqueLocked(ctx, row.NotificationID, row.RouteName, row.DestinationKey)
		if readErr != nil {
			return notification.DeliveryRow{}, false, readErr
		}
		return existing, false, nil
	}
	if err != nil {
		return notification.DeliveryRow{}, false, fmt.Errorf("insert notification delivery: %w", err)
	}
	return deliveryFromGen(got), true, nil
}

func (s *Store) ClaimDueDeliveries(ctx context.Context, sink string, owner string, now time.Time, limit int, lease time.Duration) ([]notification.DeliveryRow, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	expires := now.Add(lease)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim deliveries: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id
FROM notification_deliveries
WHERE sink = ?
  AND status IN ('queued','retry_wait')
  AND next_attempt_at <= ?
  AND attempts < max_attempts
ORDER BY next_attempt_at ASC, created_at ASC, id ASC
LIMIT ?`, sink, now, limit)
	if err != nil {
		return nil, fmt.Errorf("select due deliveries: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]notification.DeliveryRow, 0, len(ids))
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET status = 'leased',
    lease_owner = ?,
    lease_expires_at = ?,
    updated_at = ?
WHERE id = ?
  AND status IN ('queued','retry_wait')
  AND next_attempt_at <= ?
  AND attempts < max_attempts`, owner, expires, now, id, now)
		if err != nil {
			return nil, fmt.Errorf("lease delivery %s: %w", id, err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 0 {
			continue
		}
		row, err := scanDelivery(tx.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM notification_deliveries WHERE id = ?`, id))
		if err != nil {
			return nil, fmt.Errorf("read leased delivery %s: %w", id, err)
		}
		out = append(out, row)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim deliveries: %w", err)
	}
	return out, nil
}

func (s *Store) ReleaseExpiredDeliveryLeases(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.writeDB.ExecContext(ctx, `UPDATE notification_deliveries
SET attempts = attempts + 1,
    status = CASE WHEN attempts + 1 >= max_attempts THEN 'failed' ELSE 'queued' END,
    next_attempt_at = ?,
    lease_owner = '',
    lease_expires_at = NULL,
    last_error_code = 'lease_expired',
    last_error = 'delivery lease expired',
    updated_at = ?
WHERE status = 'leased'
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?`, now, now, now)
	if err != nil {
		return 0, fmt.Errorf("release expired delivery leases: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *Store) MarkDeliverySent(ctx context.Context, id string, owner string, externalID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.updateDelivery(ctx, "mark delivery sent", `UPDATE notification_deliveries
SET status = 'sent',
    attempts = attempts + 1,
    lease_owner = '',
    lease_expires_at = NULL,
    external_id = ?,
    delivered_at = ?,
    updated_at = ?
WHERE id = ?
  AND status = 'leased'
  AND lease_owner = ?
  AND lease_expires_at > ?`, externalID, at, at, id, owner, at)
}

func (s *Store) MarkDeliveryRetry(ctx context.Context, id string, owner string, errCode string, errMessage string, next time.Time, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if next.IsZero() {
		next = at
	}
	return s.updateDelivery(ctx, "mark delivery retry", `UPDATE notification_deliveries
SET attempts = attempts + 1,
    status = CASE WHEN attempts + 1 >= max_attempts THEN 'failed' ELSE 'retry_wait' END,
    next_attempt_at = ?,
    lease_owner = '',
    lease_expires_at = NULL,
    last_error_code = ?,
    last_error = ?,
    updated_at = ?
WHERE id = ?
  AND status = 'leased'
  AND lease_owner = ?
  AND lease_expires_at > ?`, next, errCode, errMessage, at, id, owner, at)
}

func (s *Store) MarkDeliveryFailed(ctx context.Context, id string, owner string, errCode string, errMessage string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.updateDelivery(ctx, "mark delivery failed", `UPDATE notification_deliveries
SET status = 'failed',
    attempts = CASE WHEN status = 'leased' THEN attempts + 1 ELSE attempts END,
    lease_owner = '',
    lease_expires_at = NULL,
    last_error_code = ?,
    last_error = ?,
    updated_at = ?
WHERE id = ?
  AND status = 'leased'
  AND lease_owner = ?
  AND lease_expires_at > ?`, errCode, errMessage, at, id, owner, at)
}

func (s *Store) MarkDeliverySkipped(ctx context.Context, id string, reason string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.updateDelivery(ctx, "mark delivery skipped", `UPDATE notification_deliveries
SET status = 'skipped',
    lease_owner = '',
    lease_expires_at = NULL,
    last_error_code = 'skipped',
    last_error = ?,
    updated_at = ?
WHERE id = ? AND status NOT IN ('sent','failed','skipped','cancelled')`, reason, at, id)
}

func (s *Store) GetDelivery(ctx context.Context, id string) (notification.DeliveryRow, bool, error) {
	row, err := s.qr.GetNotificationDelivery(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return notification.DeliveryRow{}, false, nil
	}
	if err != nil {
		return notification.DeliveryRow{}, false, fmt.Errorf("get notification delivery %s: %w", id, err)
	}
	return deliveryFromGen(row), true, nil
}

func (s *Store) ListDeliveries(ctx context.Context, filter DeliveryFilter) ([]notification.DeliveryRow, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}
	base := `SELECT ` + deliveryColumns + ` FROM notification_deliveries`
	order := ` ORDER BY created_at ASC, id ASC LIMIT ?`
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case filter.NotificationID != "":
		if filter.Status != "" {
			rows, err = s.readDB.QueryContext(ctx, base+` WHERE notification_id = ? AND status = ?`+order, filter.NotificationID, string(filter.Status), limit)
		} else {
			rows, err = s.readDB.QueryContext(ctx, base+` WHERE notification_id = ?`+order, filter.NotificationID, limit)
		}
	case filter.ProjectID != "":
		if filter.Status != "" {
			rows, err = s.readDB.QueryContext(ctx, base+` WHERE project_id = ? AND status = ?`+order, filter.ProjectID, string(filter.Status), limit)
		} else {
			rows, err = s.readDB.QueryContext(ctx, base+` WHERE project_id = ?`+order, filter.ProjectID, limit)
		}
	default:
		if filter.Status != "" {
			rows, err = s.readDB.QueryContext(ctx, base+` WHERE status = ?`+order, string(filter.Status), limit)
		} else {
			rows, err = s.readDB.QueryContext(ctx, base+order, limit)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	defer rows.Close()
	out := []notification.DeliveryRow{}
	for rows.Next() {
		row, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) updateDelivery(ctx context.Context, what string, query string, args ...any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", what, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: %w", what, notification.ErrDeliveryUpdateConflict)
	}
	return nil
}

func (s *Store) getDeliveryByUniqueLocked(ctx context.Context, id domain.NotificationID, routeName, destinationKey string) (notification.DeliveryRow, error) {
	row, err := s.qw.GetNotificationDeliveryByUnique(ctx, gen.GetNotificationDeliveryByUniqueParams{
		NotificationID: string(id),
		RouteName:      routeName,
		DestinationKey: destinationKey,
	})
	if err != nil {
		return notification.DeliveryRow{}, fmt.Errorf("get notification delivery by unique key: %w", err)
	}
	return deliveryFromGen(row), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDelivery(scanner rowScanner) (notification.DeliveryRow, error) {
	var (
		row            notification.DeliveryRow
		notificationID string
		projectID      string
		sessionID      string
		status         string
		requestJSON    string
		leaseExpires   sql.NullTime
		deliveredAt    sql.NullTime
	)
	if err := scanner.Scan(
		&row.ID,
		&notificationID,
		&row.NotificationSeq,
		&projectID,
		&sessionID,
		&row.RouteName,
		&row.Sink,
		&row.DestinationKey,
		&requestJSON,
		&status,
		&row.Attempts,
		&row.MaxAttempts,
		&row.NextAttemptAt,
		&row.LeaseOwner,
		&leaseExpires,
		&row.LastErrorCode,
		&row.LastError,
		&row.ExternalID,
		&row.CreatedAt,
		&row.UpdatedAt,
		&deliveredAt,
	); err != nil {
		return notification.DeliveryRow{}, err
	}
	row.NotificationID = domain.NotificationID(notificationID)
	row.ProjectID = domain.ProjectID(projectID)
	row.SessionID = domain.SessionID(sessionID)
	row.RequestJSON = []byte(requestJSON)
	row.Status = notification.DeliveryStatus(status)
	if leaseExpires.Valid {
		row.LeaseExpiresAt = leaseExpires.Time
	}
	if deliveredAt.Valid {
		row.DeliveredAt = deliveredAt.Time
	}
	return row, nil
}

func deliveryFromGen(r gen.NotificationDelivery) notification.DeliveryRow {
	row := notification.DeliveryRow{
		ID:              r.ID,
		NotificationID:  domain.NotificationID(r.NotificationID),
		NotificationSeq: r.NotificationSeq,
		ProjectID:       domain.ProjectID(r.ProjectID),
		SessionID:       domain.SessionID(r.SessionID),
		RouteName:       r.RouteName,
		Sink:            r.Sink,
		DestinationKey:  r.DestinationKey,
		RequestJSON:     []byte(r.RequestJson),
		Status:          notification.DeliveryStatus(r.Status),
		Attempts:        int(r.Attempts),
		MaxAttempts:     int(r.MaxAttempts),
		NextAttemptAt:   r.NextAttemptAt,
		LeaseOwner:      r.LeaseOwner,
		LastErrorCode:   r.LastErrorCode,
		LastError:       r.LastError,
		ExternalID:      r.ExternalID,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
	if r.LeaseExpiresAt.Valid {
		row.LeaseExpiresAt = r.LeaseExpiresAt.Time
	}
	if r.DeliveredAt.Valid {
		row.DeliveredAt = r.DeliveredAt.Time
	}
	return row
}
