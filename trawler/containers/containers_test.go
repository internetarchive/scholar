package containers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"
)

func Test_CleanName(t *testing.T) {
	require.Equal(t, "Nihon Shouni Jinzoubyou Gakkai zasshi",
		CleanName("  Nihon   Shouni Jinzoubyou Gakkai zasshi "))
	require.Equal(t, "", CleanName("  "))
}

func nopLogger() log.Logger {
	return log.NewStructuredLogger(slog.New(slog.DiscardHandler))
}

// scripted is a row that says what should happen to it: "added", "ignored",
// "skipped", "err" (or "added-nol" for a container created without an ISSN-L).
type scripted string

func (scripted) ToContainer(string) (fatcat2.Container, []string, string) {
	return fatcat2.Container{}, nil, ""
}

func (s scripted) LogValues() []any { return []any{"scripted", string(s)} }

func rowsOf(ss ...string) []Row {
	out := make([]Row, len(ss))
	for i, s := range ss {
		out[i] = scripted(s)
	}
	return out
}

func scriptedEnsure(seen *[]string) func(*http.Client, Row, string) (Outcome, *fatcat2.Container, error) {
	return func(_ *http.Client, row Row, _ string) (Outcome, *fatcat2.Container, error) {
		s := string(row.(scripted))
		*seen = append(*seen, s)
		switch s {
		case "added":
			return Added, &fatcat2.Container{ISSNL: "0915-2245"}, nil
		case "added-nol":
			return Added, &fatcat2.Container{}, nil
		case "ignored":
			return Ignored, nil, nil
		case "skipped":
			return Skipped, nil, nil
		}
		return "", nil, errors.New("boom")
	}
}

func Test_ImportRows_Tallies(t *testing.T) {
	var seen []string
	var last Progress
	counts, err := importRows(context.Background(), nopLogger(), nil,
		rowsOf("added", "added-nol", "ignored", "ignored", "skipped", "err", "ignored"),
		"src", 3, Progress{},
		func(p Progress) { last = p },
		scriptedEnsure(&seen))
	require.NoError(t, err)
	require.Equal(t, Counts{Rows: 7, Added: 2, AddedWithoutISSNL: 1, Ignored: 3, Skipped: 1, Errored: 1}, counts)
	require.Equal(t, Progress{Next: 7, Counts: counts}, last)
}

func Test_ImportRows_ResumesFromCheckpoint(t *testing.T) {
	var seen []string
	prior := Counts{Rows: 2, Ignored: 2}
	counts, err := importRows(context.Background(), nopLogger(), nil,
		rowsOf("ignored", "ignored", "added", "ignored"),
		"src", 3, Progress{Next: 2, Counts: prior},
		func(Progress) {},
		scriptedEnsure(&seen))
	require.NoError(t, err)
	require.Equal(t, []string{"added", "ignored"}, seen)
	require.Equal(t, Counts{Rows: 4, Added: 1, Ignored: 3}, counts)
}

// A streak of failures aborts, checkpointing back to where the streak began
// so the retry redoes those rows instead of writing them off.
func Test_ImportRows_StreakAbortsAndRewinds(t *testing.T) {
	var seen []string
	var last Progress
	_, err := importRows(context.Background(), nopLogger(), nil,
		rowsOf("ignored", "err", "ignored", "err", "err", "err", "ignored"),
		"src", 3, Progress{},
		func(p Progress) { last = p },
		scriptedEnsure(&seen))
	require.ErrorContains(t, err, "3 consecutive rows failed")
	require.Equal(t, Progress{Next: 3, Counts: Counts{Rows: 3, Ignored: 2, Errored: 1}}, last)
	require.Len(t, seen, 6, "rows after the abort are not attempted")
}
