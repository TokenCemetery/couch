package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// AnalysisLimit is the rows per index PRAGMA optimize examines: Emby's
// DatabaseAnalysisLimit, at its default.
const AnalysisLimit = 400

// Vacuum rebuilds the database file once when VacuumOnStartup asks for it.
// The request is cleared first, so a VACUUM that fails or is interrupted is
// not repeated at every start; when it cannot be cleared, VACUUM does not
// run. It reports whether it was asked for and the file's size before and
// after. It holds the only database connection for the whole rewrite, so it
// runs before Couch serves.
func (s *Store) Vacuum(ctx context.Context) (asked bool, before, after int64, err error) {
	if !s.Snapshot().VacuumOnStartup {
		return false, 0, 0, nil
	}
	off := false
	if err := s.SetMaintenance(&off, nil); err != nil {
		return true, 0, 0, fmt.Errorf("clear the vacuum request: %w", err)
	}
	file := filepath.Join(s.dir, "couch.db")
	if info, statErr := os.Stat(file); statErr == nil {
		before = info.Size()
	}
	_, err = s.db.ExecContext(ctx, "VACUUM")
	if info, statErr := os.Stat(file); statErr == nil {
		after = info.Size()
	}
	return true, before, after, err
}

// Optimize runs PRAGMA optimize when OptimizeOnShutdown is set, which
// usually does nothing and occasionally analyses a table for the query
// planner. It reports whether it ran.
func (s *Store) Optimize(ctx context.Context) (bool, error) {
	if !s.Snapshot().OptimizeOnShutdown {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, "PRAGMA analysis_limit = "+strconv.Itoa(AnalysisLimit)+"; PRAGMA optimize;")
	return true, err
}
