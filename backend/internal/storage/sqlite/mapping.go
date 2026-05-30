package sqlite

import (
	"database/sql"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// recordToInsert maps a domain record to the generated insert params. The
// revision column is fixed to 1 by the query itself (insert path), so it is not
// carried here.
func recordToInsert(rec domain.SessionRecord) gen.InsertSessionParams {
	lc := rec.Lifecycle
	da, ds, dh := detectingToNull(lc.Detecting)
	return gen.InsertSessionParams{
		ID:                    string(rec.ID),
		ProjectID:             string(rec.ProjectID),
		IssueID:               string(rec.IssueID),
		Kind:                  string(rec.Kind),
		CreatedAt:             rec.CreatedAt,
		UpdatedAt:             rec.UpdatedAt,
		SessionState:          string(lc.Session.State),
		SessionReason:         string(lc.Session.Reason),
		PrState:               string(lc.PR.State),
		PrReason:              string(lc.PR.Reason),
		PrNumber:              int64(lc.PR.Number),
		PrUrl:                 lc.PR.URL,
		RuntimeState:          string(lc.Runtime.State),
		RuntimeReason:         string(lc.Runtime.Reason),
		ActivityState:         string(lc.Activity.State),
		ActivityLastAt:        lc.Activity.LastActivityAt,
		ActivitySource:        string(lc.Activity.Source),
		DetectingAttempts:     da,
		DetectingStartedAt:    ds,
		DetectingEvidenceHash: dh,
	}
}

// recordToUpdate maps a domain record to the CAS update params. expectedRevision
// is the caller's loaded revision, used in the WHERE clause for the CAS check.
func recordToUpdate(rec domain.SessionRecord, expectedRevision int64) gen.UpdateSessionCASParams {
	lc := rec.Lifecycle
	da, ds, dh := detectingToNull(lc.Detecting)
	return gen.UpdateSessionCASParams{
		ProjectID:             string(rec.ProjectID),
		IssueID:               string(rec.IssueID),
		Kind:                  string(rec.Kind),
		UpdatedAt:             rec.UpdatedAt,
		SessionState:          string(lc.Session.State),
		SessionReason:         string(lc.Session.Reason),
		PrState:               string(lc.PR.State),
		PrReason:              string(lc.PR.Reason),
		PrNumber:              int64(lc.PR.Number),
		PrUrl:                 lc.PR.URL,
		RuntimeState:          string(lc.Runtime.State),
		RuntimeReason:         string(lc.Runtime.Reason),
		ActivityState:         string(lc.Activity.State),
		ActivityLastAt:        lc.Activity.LastActivityAt,
		ActivitySource:        string(lc.Activity.Source),
		DetectingAttempts:     da,
		DetectingStartedAt:    ds,
		DetectingEvidenceHash: dh,
		ID:                    string(rec.ID),
		Revision:              expectedRevision,
	}
}

// rowToRecord maps a stored session row back to a domain record. Metadata is
// deliberately left nil: it is a side-channel (session_metadata) read only by
// GetMetadata, never reconstructed here — mirroring the in-memory fakeStore.
func rowToRecord(row gen.Session) domain.SessionRecord {
	return domain.SessionRecord{
		ID:        domain.SessionID(row.ID),
		ProjectID: domain.ProjectID(row.ProjectID),
		IssueID:   domain.IssueID(row.IssueID),
		Kind:      domain.SessionKind(row.Kind),
		Lifecycle: rowToLifecycle(row),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func rowToLifecycle(row gen.Session) domain.CanonicalSessionLifecycle {
	return domain.CanonicalSessionLifecycle{
		Version:  domain.LifecycleVersion,
		Revision: int(row.Revision),
		Session: domain.SessionSubstate{
			State:  domain.SessionState(row.SessionState),
			Reason: domain.SessionReason(row.SessionReason),
		},
		PR: domain.PRSubstate{
			State:  domain.PRState(row.PrState),
			Reason: domain.PRReason(row.PrReason),
			Number: int(row.PrNumber),
			URL:    row.PrUrl,
		},
		Runtime: domain.RuntimeSubstate{
			State:  domain.RuntimeState(row.RuntimeState),
			Reason: domain.RuntimeReason(row.RuntimeReason),
		},
		Activity: domain.ActivitySubstate{
			State:          domain.ActivityState(row.ActivityState),
			LastActivityAt: row.ActivityLastAt,
			Source:         domain.ActivitySource(row.ActivitySource),
		},
		Detecting: nullToDetecting(row),
	}
}

func detectingToNull(d *domain.DetectingState) (sql.NullInt64, sql.NullTime, sql.NullString) {
	if d == nil {
		return sql.NullInt64{}, sql.NullTime{}, sql.NullString{}
	}
	return sql.NullInt64{Int64: int64(d.Attempts), Valid: true},
		sql.NullTime{Time: d.StartedAt, Valid: true},
		sql.NullString{String: d.EvidenceHash, Valid: true}
}

func nullToDetecting(row gen.Session) *domain.DetectingState {
	if !row.DetectingAttempts.Valid {
		return nil
	}
	return &domain.DetectingState{
		Attempts:     int(row.DetectingAttempts.Int64),
		StartedAt:    row.DetectingStartedAt.Time,
		EvidenceHash: row.DetectingEvidenceHash.String,
	}
}
