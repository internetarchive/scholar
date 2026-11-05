// Package containers holds what the container importers (DOAJ's journal list,
// OpenAlex's sources) share: finding, creating and indexing a container, and
// the resumable loop that walks an upstream's rows making sure fatcat has a
// container for each. An importer supplies its rows as Rows.
//
// The importers are create-only. A row whose journal fatcat already has is
// left alone; existing containers may have been curated, and no one upstream
// is the authority for most of them.
//
// Bringing existing containers up to date from an upstream worth trusting for
// some part of them (EZB) is a separate, deliberate step: see UpdateExisting.
package containers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

// CleanName tidies a journal or publisher name for fatcat. CleanString fixes
// punctuation but leaves whitespace alone, and upstream names carry stray runs
// of it.
func CleanName(s string) string {
	return cleaning.CleanString(strings.Join(strings.Fields(s), " "))
}

// CreateAndIndex creates c in fatcat2 and indexes it to Elasticsearch.
func CreateAndIndex(client *http.Client, c *fatcat2.Container) error {
	if _, err := fatcat2.CreateContainer(client, c); err != nil {
		return fmt.Errorf("container creation failed for %q: %w", c.Name, err)
	}

	// NB as in the article importers, a failure here is not revisited: the next
	// run finds the container in fatcat and leaves it alone.
	return index(client, c)
}

// index sends c to the container index in Elasticsearch.
func index(client *http.Client, c *fatcat2.Container) error {
	doc := indexing.PrepareFatcatContainerDoc(*c)
	bs, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("could not marshal container doc: %w", err)
	}
	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_container_ix"), doc.LegacyIdent, bs)
	if err != nil {
		return fmt.Errorf("could not index container %q: %w", c.ID, err)
	}

	return nil
}

// Outcome is what happened to a single row.
type Outcome string

const (
	Added   Outcome = "added"
	Ignored Outcome = "ignored"
	Skipped Outcome = "skipped"
)

// Counts is the tally for one import.
//
// It is a workflow result and part of the heartbeat checkpoint, so its fields
// are a Temporal payload: renaming one breaks decoding for runs in flight.
type Counts struct {
	// Rows is how many rows reached a verdict.
	Rows int
	// Added is how many containers were created.
	Added int
	// AddedWithoutISSNL is how many of Added have no ISSN-L, because their
	// ISSNs are too new for the ISSN-L table. They can be found by ISSN-E or
	// ISSN-P only.
	AddedWithoutISSNL int
	// Ignored is how many rows fatcat already had a container for.
	Ignored int
	// Skipped is how many rows could not become a container.
	Skipped int
	// Errored is how many rows failed and were passed over. They are retried
	// on the next run, which tries every row again.
	Errored int
}

// Progress is the heartbeat checkpoint: the next row to import, and the tally
// of every row before it.
type Progress struct {
	Next   int
	Counts Counts
}

// ResumeProgress returns where a retried import activity left off, or the
// start if this is the first attempt.
func ResumeProgress(ctx context.Context) Progress {
	l := activity.GetLogger(ctx)
	var progress Progress
	if !activity.HasHeartbeatDetails(ctx) {
		return progress
	}
	if err := activity.GetHeartbeatDetails(ctx, &progress); err != nil {
		l.Warn("could not read heartbeat checkpoint; starting from the top", "error", err)
		return Progress{}
	}
	l.Info("resuming container import", "row", progress.Next)
	return progress
}

// Row is one upstream record that may become a fatcat container.
type Row interface {
	// ToContainer returns the container this row would become, stamped with
	// source, and every ISSN to look for an existing one by. A non-empty skip
	// reason means the row cannot become a container, and the other two are
	// meaningless.
	ToContainer(source string) (c fatcat2.Container, lookup []string, skip string)
	// LogValues identifies the row in a log line, as key/value pairs.
	LogValues() []any
}

// Find returns the ID of a fatcat2 container carrying any of issns, if there is
// one. Every ISSN is tried against every ISSN column, because nothing
// guarantees which column an existing container filed it under: legacy
// containers often carry only an ISSN-L, and an ISSN-L is itself one of the
// journal's ISSNs.
//
// Put ISSN-Ls first. That is how nearly every existing container is found, so
// a journal fatcat has had for years costs one request.
func Find(client *http.Client, issns []string) (*uuid.UUID, error) {
	id, err := fatcat2.LookupContainerByISSNs(client, issns...)
	if err != nil {
		return nil, fmt.Errorf("container lookup failed: %w", err)
	}
	return id, nil
}

// Ensure makes sure fatcat has a container for row, creating and indexing one
// if it does not. On Added it returns the container it created.
//
// It is idempotent, which is what makes re-running an import, or resuming one
// part way, safe.
func Ensure(client *http.Client, row Row, source string) (Outcome, *fatcat2.Container, error) {
	c, lookup, skip := row.ToContainer(source)
	if skip != "" {
		return Skipped, nil, nil
	}

	existing, err := Find(client, lookup)
	if err != nil {
		return "", nil, err
	}
	if existing != nil {
		return Ignored, nil, nil
	}

	if err := CreateAndIndex(client, &c); err != nil {
		return "", nil, err
	}

	return Added, &c, nil
}

// UpdateOutcome is what UpdateExisting did with a row.
type UpdateOutcome string

const (
	// Updated is a strongly matched container the row changed.
	Updated UpdateOutcome = "updated"
	// Unchanged is a strongly matched container the row had nothing to add to.
	Unchanged UpdateOutcome = "unchanged"
	// NotFound is a row fatcat has no container for. Nothing is created.
	NotFound UpdateOutcome = "not-found"
	// Conflicted is a container found by ISSN but not trusted to be the row's
	// journal, and left alone; see StrongMatch.
	Conflicted UpdateOutcome = "conflicted"
	// Unusable is a row with nothing to look a container up by.
	Unusable UpdateOutcome = "unusable"
)

// Updater is an upstream record that may bring fatcat's existing container for
// its journal up to date.
type Updater interface {
	// Lookup returns every ISSN to find the row's container by, ISSN-Ls first
	// (see Find). A non-empty skip reason means the row cannot be matched to a
	// container, and the ISSNs are meaningless.
	Lookup() (issns []string, skip string)
	// Update applies the row to doc, an existing container as fatcat2 serves it
	// (see fatcat2.GetContainerDoc). It is only called on a strong match.
	//
	// It should touch only what the upstream speaks for -- its own key in extra,
	// and fields the container has no value for -- and must be idempotent: a
	// second application of the same row changes nothing. UpdateExisting writes
	// only when doc differs afterwards, so that is what keeps a re-run from
	// rewriting every container it matches.
	Update(doc map[string]any)
	// LogValues identifies the row in a log line, as key/value pairs.
	LogValues() []any
}

// StrongMatch reports whether doc, a container found by one of issns, can be
// trusted to be the journal issns describe: every ISSN it carries is one of
// issns, or belongs to the ISSN-L group of one of them.
//
// A lookup by ISSN finds a container that shares any one ISSN with the row.
// That is enough for an importer to decline to create a duplicate, but not to
// write to it: legacy containers merged across title changes, and upstreams
// fold a journal's earlier titles into one record, so a shared ISSN can join
// two journals.
func StrongMatch(doc map[string]any, issns []string) bool {
	group := map[string]bool{}
	for _, i := range issns {
		if i != "" {
			group[i] = true
		}
	}

	shared := false
	for _, k := range []string{"issnl", "issnp", "issne"} {
		raw, _ := doc[k].(string)
		if raw == "" {
			continue
		}
		i := issn.Normalize(raw)
		if i == "" {
			// An ISSN we cannot even read is not one we can vouch for.
			return false
		}
		if !group[i] && !group[issn.ISSN2ISSNL(i)] {
			return false
		}
		shared = true
	}
	return shared
}

// UpdateExisting brings fatcat's container for row up to date with it, if
// fatcat has one and it strongly matches, writing it back to fatcat2 and
// re-indexing it. It never creates a container. With dryRun it decides the
// outcome but writes nothing.
//
// On Updated it returns the container as updated.
func UpdateExisting(client *http.Client, row Updater, dryRun bool) (UpdateOutcome, *fatcat2.Container, error) {
	lookup, skip := row.Lookup()
	if skip != "" {
		return Unusable, nil, nil
	}

	existing, err := Find(client, lookup)
	if err != nil {
		return "", nil, err
	}
	if existing == nil {
		return NotFound, nil, nil
	}

	doc, err := fatcat2.GetContainerDoc(client, *existing)
	if err != nil {
		return "", nil, err
	}
	if !StrongMatch(doc, lookup) {
		return Conflicted, nil, nil
	}

	// Compared as JSON, which sorts map keys and spells a number the same
	// whether it arrived as a float64 or Update put in an int.
	before, err := json.Marshal(doc)
	if err != nil {
		return "", nil, fmt.Errorf("could not marshal container %s: %w", existing, err)
	}
	row.Update(doc)
	after, err := json.Marshal(doc)
	if err != nil {
		return "", nil, fmt.Errorf("could not marshal updated container %s: %w", existing, err)
	}
	if bytes.Equal(before, after) {
		return Unchanged, nil, nil
	}

	var updated fatcat2.Container
	if err := json.Unmarshal(after, &updated); err != nil {
		return "", nil, fmt.Errorf("could not read back updated container %s: %w", existing, err)
	}
	if dryRun {
		return Updated, &updated, nil
	}

	if err := fatcat2.PutContainerDoc(client, doc); err != nil {
		return "", nil, err
	}
	// Without this, the container's search doc keeps its old publisher and
	// ISSNs. A failed index is not revisited by a re-run, which finds the
	// container already current and does not write it again.
	if err := index(client, &updated); err != nil {
		return "", nil, err
	}

	return Updated, &updated, nil
}

// ImportRows makes sure fatcat has a container for each of rows from
// progress.Next on, heartbeating a checkpoint after each.
//
// A row that fails is counted and passed over; one bad row must not hold up
// thousands of others, and the next run will try it again. A long unbroken run
// of failures is something else -- fatcat or ES being down -- and fails the
// activity so Temporal can retry it once things recover. The retry starts from
// the beginning of that streak, not after it.
func ImportRows(
	ctx context.Context,
	l log.Logger,
	client *http.Client,
	rows []Row,
	source string,
	maxConsecutive int,
	progress Progress,
	heartbeat func(Progress),
) (Counts, error) {
	return importRows(ctx, l, client, rows, source, maxConsecutive, progress, heartbeat, Ensure)
}

// importRows is ImportRows with the per-row step passed in, so the loop can be
// tested without fatcat.
func importRows(
	ctx context.Context,
	l log.Logger,
	client *http.Client,
	rows []Row,
	source string,
	maxConsecutive int,
	progress Progress,
	heartbeat func(Progress),
	ensure func(*http.Client, Row, string) (Outcome, *fatcat2.Container, error),
) (Counts, error) {
	counts := progress.Counts

	// Where the current error streak began, and the tally at that point, so an
	// abort can checkpoint back to before it.
	streak := 0
	streakStart := progress
	for i := progress.Next; i < len(rows); i++ {
		if err := ctx.Err(); err != nil {
			return counts, err
		}

		before := counts
		outcome, c, err := ensure(client, rows[i], source)
		counts.Rows++
		if err != nil {
			if streak == 0 {
				streakStart = Progress{Next: i, Counts: before}
			}
			streak++
			counts.Errored++
			l.Warn("container import failed", append([]any{"row", i, "error", err}, rows[i].LogValues()...)...)
			if streak >= maxConsecutive {
				heartbeat(streakStart)
				return counts, fmt.Errorf("%d consecutive rows failed, last at row %d: %w", streak, i, err)
			}
		} else {
			streak = 0
			switch outcome {
			case Added:
				counts.Added++
				if c != nil && c.ISSNL == "" {
					counts.AddedWithoutISSNL++
				}
				l.Info("created container", "id", c.ID, "name", c.Name,
					"issnl", c.ISSNL, "issnp", c.ISSNP, "issne", c.ISSNE)
			case Ignored:
				counts.Ignored++
			case Skipped:
				counts.Skipped++
			}
		}

		heartbeat(Progress{Next: i + 1, Counts: counts})
	}

	return counts, nil
}
