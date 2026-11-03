package state

import (
	"context"
	"time"
)

// MetadataRecord is the stored metadata of one catalogue item, or of a
// person the items credit, keyed by the person's ID. The state package
// keeps the bodies opaque; package metadata encodes them.
type MetadataRecord struct {
	ItemID string
	// NFORevision identifies the .nfo file NFO was read from; empty when
	// the item has none.
	NFORevision string
	NFO         []byte
	// OnlineKey identifies the lookup Online answers: the providers, the
	// language and what was looked up. Empty when nothing was fetched.
	OnlineKey string
	Online    []byte
	FetchedAt time.Time
	// Identified holds the provider IDs an administrator chose with
	// Identify; empty when none.
	Identified []byte
}

// Metadata returns every stored metadata record by item ID.
func (s *Store) Metadata(ctx context.Context) (map[string]MetadataRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT item_id, nfo_revision, nfo, online_key, online, fetched_at, identified FROM metadata")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := map[string]MetadataRecord{}
	for rows.Next() {
		var r MetadataRecord
		var nfo, online, fetched, identified string
		if err := rows.Scan(&r.ItemID, &r.NFORevision, &nfo, &r.OnlineKey, &online, &fetched, &identified); err != nil {
			return nil, err
		}
		if err := r.FetchedAt.UnmarshalText([]byte(fetched)); err != nil {
			return nil, err
		}
		r.NFO, r.Online = []byte(nfo), []byte(online)
		if identified != "" {
			r.Identified = []byte(identified)
		}
		records[r.ItemID] = r
	}
	return records, rows.Err()
}

// SaveMetadata writes records and deletes the records of remove in one
// transaction.
func (s *Store) SaveMetadata(ctx context.Context, records []MetadataRecord, remove []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range records {
		fetched, err := timeText(r.FetchedAt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata (item_id, nfo_revision, nfo, online_key, online, fetched_at, identified) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (item_id) DO UPDATE SET nfo_revision = excluded.nfo_revision, nfo = excluded.nfo,
				online_key = excluded.online_key, online = excluded.online, fetched_at = excluded.fetched_at, identified = excluded.identified`,
			r.ItemID, r.NFORevision, string(r.NFO), r.OnlineKey, string(r.Online), fetched, string(r.Identified)); err != nil {
			return err
		}
	}
	for _, id := range remove {
		if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE item_id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
