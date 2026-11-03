package cassandra

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// The account columns are only written when set, so a producer that does not
// send them cannot overwrite (null out) values already stored on the row.
func TestV3InsertStatements_AccountColumns(t *testing.T) {
	now := time.Now()
	base := data_access.EventV3UpsertRecord{Namespace: "ns", Context: "c", EventName: "e", Source: "s", Timestamp: now}

	t.Run("legacy record omits both columns", func(t *testing.T) {
		q, args := eventsV3InsertStatement(base, now, now)
		assert.NotContains(t, q, "nca_id")
		assert.NotContains(t, q, "function_id")
		assert.Len(t, args, 8)

		q, args = statsV3InsertStatement(base, now, now)
		assert.NotContains(t, q, "nca_id")
		assert.Len(t, args, 6)
	})

	t.Run("account record writes both columns", func(t *testing.T) {
		rec := base
		rec.NcaID = "acct-1"
		rec.FunctionID = "fn-1"

		q, args := eventsV3InsertStatement(rec, now, now)
		assert.Contains(t, q, ", nca_id, function_id)")
		assert.Contains(t, q, "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		assert.Equal(t, []any{"acct-1", "fn-1"}, args[8:])

		q, args = statsV3InsertStatement(rec, now, now)
		assert.Contains(t, q, "INTO stats_v3 (")
		assert.Contains(t, q, ", nca_id, function_id)")
		assert.Equal(t, []any{"acct-1", "fn-1"}, args[6:])
	})

	t.Run("nca only", func(t *testing.T) {
		rec := base
		rec.NcaID = "acct-1"
		q, args := eventsV3InsertStatement(rec, now, now)
		assert.Contains(t, q, ", nca_id)")
		assert.NotContains(t, q, "function_id")
		assert.Len(t, args, 9)
	})
}
