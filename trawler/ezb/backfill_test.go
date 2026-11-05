package ezb

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers/containerstest"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// setupBackfillFatcat has containers for the nephrology journal and, with
// another journal's ISSN on it, Analytical Communications. Footprint has none.
func setupBackfillFatcat(t *testing.T) (*containerstest.Fatcat, uuid.UUID) {
	neph, anal := uuid.New(), uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{
		"issnl=0915-2245": neph,
		"issnl=1359-7337": anal,
	})
	f.Docs[neph] = map[string]any{"id": neph.String(), "name": "Nihon Shouni Jinzoubyou Gakkai zasshi",
		"issnl": "0915-2245", "extra": map[string]any{}}
	f.Docs[anal] = map[string]any{"id": anal.String(), "name": "Analytical Communications",
		"issnl": "1359-7337", "issne": "1881-3933"}
	return f, neph
}

func backfillInput() *strings.Reader {
	return strings.NewReader(list(nephrology, openAccess, noISSN, analComm1, analComm2))
}

func Test_Backfill_DryRun(t *testing.T) {
	f, neph := setupBackfillFatcat(t)

	var updated, conflicted bytes.Buffer
	counts, err := Backfill(context.Background(), http.DefaultClient, backfillInput(), false, 3,
		BackfillOutput{Updated: &updated, Conflicted: &conflicted}, nil)
	require.NoError(t, err)

	require.Equal(t, Counts{Rows: 4, Updated: 1, NotFound: 1, Conflicted: 1, Unusable: 1}, counts)
	require.Empty(t, f.Put)
	require.Empty(t, f.Created)
	require.Equal(t, 0, f.Indexed)

	var u fatcat2.Container
	require.NoError(t, json.Unmarshal(updated.Bytes(), &u))
	require.Equal(t, neph, u.ID)
	require.Equal(t, "1881-3933", u.ISSNE)

	var c conflictRecord
	require.NoError(t, json.Unmarshal(conflicted.Bytes(), &c))
	require.Equal(t, conflictRecord{EZBIDs: []int{4, 39960}, Title: "Analytical Communications",
		ISSNL: "1359-7337", ISSNs: []string{"1359-7337", "1364-5536"}}, c)
}

func Test_Backfill_Apply(t *testing.T) {
	f, neph := setupBackfillFatcat(t)

	counts, err := Backfill(context.Background(), http.DefaultClient, backfillInput(), true, 3,
		BackfillOutput{}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, counts.Updated)
	require.Len(t, f.Put, 1)
	require.Equal(t, neph.String(), f.Put[0]["id"])
	require.Equal(t, 1, f.Indexed)
	require.Empty(t, f.Created, "a journal fatcat lacks is never created")

	// Run again: already current, nothing written.
	counts, err = Backfill(context.Background(), http.DefaultClient, backfillInput(), true, 3,
		BackfillOutput{}, nil)
	require.NoError(t, err)
	require.Equal(t, Counts{Rows: 4, Unchanged: 1, NotFound: 1, Conflicted: 1, Unusable: 1}, counts)
	require.Len(t, f.Put, 1)
}
