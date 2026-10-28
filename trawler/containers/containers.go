// Package containers holds what the container importers (DOAJ's journal list,
// OpenAlex's sources) share: finding, creating and indexing a container, and
// the resumable loop that walks an upstream's rows making sure fatcat has a
// container for each. An importer supplies its rows as Rows.
//
// The importers are create-only. A row whose journal fatcat already has is
// left alone; existing containers may have been curated, and no one upstream
// is the authority for most of them.
package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
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
