package containers

import (
	"net/http"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers/containerstest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_StrongMatch(t *testing.T) {
	// 0915-2245 (print) and 1881-3933 (online) share the ISSN-L 0915-2245.
	journal := []string{"0915-2245", "0915-2245", "1881-3933"}
	cases := []struct {
		name string
		doc  map[string]any
		want bool
	}{
		{"legacy container with only the ISSN-L",
			map[string]any{"issnl": "0915-2245"}, true},
		{"every ISSN is the row's",
			map[string]any{"issnl": "0915-2245", "issnp": "0915-2245", "issne": "1881-3933"}, true},
		{"an ISSN the row does not list, but in its ISSN-L group",
			map[string]any{"issne": "1881-3933"}, true},
		{"an ISSN from another journal",
			map[string]any{"issnl": "0915-2245", "issne": "1364-5536"}, false},
		{"an unreadable ISSN",
			map[string]any{"issnl": "0915-2245", "issnp": "junk"}, false},
		{"no ISSNs at all",
			map[string]any{"name": "A Journal"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, StrongMatch(c.doc, journal))
		})
	}

	require.True(t, StrongMatch(map[string]any{"issne": "1364-548x"}, []string{"1364-548X"}),
		"a stored ISSN with a lowercase check digit still matches")
}

// updaterRow is an Updater that sets extra.test to its value.
type updaterRow struct {
	issns []string
	value string
	skip  string
}

func (r updaterRow) Lookup() ([]string, string) { return r.issns, r.skip }

func (r updaterRow) LogValues() []any { return nil }

func (r updaterRow) Update(doc map[string]any) {
	extra, _ := doc["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
		doc["extra"] = extra
	}
	extra["test"] = r.value
}

// existingDoc is a container as fc2 serves it, with fields fatcat2.Container
// does not carry.
func existingDoc(id uuid.UUID) map[string]any {
	return map[string]any{
		"id":           id.String(),
		"name":         "A Journal",
		"issnl":        "0915-2245",
		"source":       "chocula",
		"created":      "2019-01-29T00:00:00Z",
		"wikidata_qid": "Q123",
		"extra":        map[string]any{"doaj": map[string]any{"as_of": "2022-07-06"}, "test": "old"},
	}
}

// An update writes back every field fc2 served, not just the ones
// fatcat2.Container knows, leaves the container's source alone, and
// re-indexes it.
func Test_UpdateExisting_UpdatesStrongMatchKeepingEverythingElse(t *testing.T) {
	id := uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": id})
	f.Docs[id] = existingDoc(id)

	outcome, c, err := UpdateExisting(http.DefaultClient, updaterRow{issns: []string{"0915-2245"}, value: "new"}, false)
	require.NoError(t, err)
	require.Equal(t, Updated, outcome)
	require.Equal(t, id, c.ID)
	require.Equal(t, 1, f.Indexed)

	require.Len(t, f.Put, 1)
	want := existingDoc(id)
	want["extra"].(map[string]any)["test"] = "new"
	require.Equal(t, want, f.Put[0])
}

// A second run with nothing new upstream writes nothing.
func Test_UpdateExisting_UnchangedIsNotWritten(t *testing.T) {
	id := uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": id})
	f.Docs[id] = existingDoc(id)
	row := updaterRow{issns: []string{"0915-2245"}, value: "new"}

	_, _, err := UpdateExisting(http.DefaultClient, row, false)
	require.NoError(t, err)
	outcome, _, err := UpdateExisting(http.DefaultClient, row, false)
	require.NoError(t, err)
	require.Equal(t, Unchanged, outcome)
	require.Len(t, f.Put, 1)
	require.Equal(t, 1, f.Indexed)
}

// Nothing is ever created.
func Test_UpdateExisting_NotFound(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := UpdateExisting(http.DefaultClient, updaterRow{issns: []string{"0915-2245"}, value: "new"}, false)
	require.NoError(t, err)
	require.Equal(t, NotFound, outcome)
	require.Nil(t, c)
	require.Empty(t, f.Created)
	require.Empty(t, f.Put)
}

func Test_UpdateExisting_ConflictIsLeftAlone(t *testing.T) {
	id := uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": id})
	doc := existingDoc(id)
	doc["issne"] = "1364-5536" // another journal's ISSN
	f.Docs[id] = doc

	outcome, _, err := UpdateExisting(http.DefaultClient, updaterRow{issns: []string{"0915-2245"}, value: "new"}, false)
	require.NoError(t, err)
	require.Equal(t, Conflicted, outcome)
	require.Empty(t, f.Put)
}

func Test_UpdateExisting_Unusable(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, _, err := UpdateExisting(http.DefaultClient, updaterRow{skip: "no-valid-issn"}, false)
	require.NoError(t, err)
	require.Equal(t, Unusable, outcome)
	require.Empty(t, f.Lookups)
}

func Test_UpdateExisting_DryRunWritesNothing(t *testing.T) {
	id := uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": id})
	f.Docs[id] = existingDoc(id)

	outcome, c, err := UpdateExisting(http.DefaultClient, updaterRow{issns: []string{"0915-2245"}, value: "new"}, true)
	require.NoError(t, err)
	require.Equal(t, Updated, outcome)
	require.Equal(t, "new", c.Extra["test"], "the container as it would be")

	require.Empty(t, f.Put)
	require.Equal(t, 0, f.Indexed)
}
