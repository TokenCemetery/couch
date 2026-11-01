package state

import (
	"context"
	"time"
)

// maxActivity bounds the activity log; older entries are dropped.
const maxActivity = 1000

// activityDate is fixed-width, so dates compare as text.
const activityDate = "2006-01-02T15:04:05.000000000Z"

// Activity is an entry of the activity log the dashboard shows.
type Activity struct {
	ID       int64
	Date     time.Time
	Type     string
	Name     string
	Overview string
	// Severity is Info, Warn or Error.
	Severity string
	UserID   string
	ItemID   string
}

// AddActivity records an entry and drops the oldest beyond maxActivity.
func (s *Store) AddActivity(ctx context.Context, a Activity) error {
	date := a.Date.UTC().Format(activityDate)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO activity (date, type, name, overview, severity, user_id, item_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		date, a.Type, a.Name, a.Overview, a.Severity, a.UserID, a.ItemID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM activity WHERE id <= (SELECT max(id) FROM activity) - ?", maxActivity); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ActivityLog returns the entries from minDate on, newest first, a page of
// them from start, and how many there are.
func (s *Store) ActivityLog(ctx context.Context, minDate time.Time, start, limit int) ([]Activity, int, error) {
	after := minDate.UTC().Format(activityDate)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM activity WHERE date >= ?", after).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, date, type, name, overview, severity, user_id, item_id FROM activity WHERE date >= ? ORDER BY id DESC LIMIT ? OFFSET ?",
		after, limit, start)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	entries := []Activity{}
	for rows.Next() {
		var a Activity
		var date string
		if err := rows.Scan(&a.ID, &date, &a.Type, &a.Name, &a.Overview, &a.Severity, &a.UserID, &a.ItemID); err != nil {
			return nil, 0, err
		}
		if a.Date, err = time.Parse(activityDate, date); err != nil {
			return nil, 0, err
		}
		entries = append(entries, a)
	}
	return entries, total, rows.Err()
}
