package ezb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"git.archive.org/webgroup/scholar/trawler/containers"
)

// Counts is the tally of a backfill.
type Counts struct {
	// Rows is how many journals (EZB records, grouped) were looked at.
	Rows int
	// Updated is how many containers were, or in a dry run would be, changed.
	Updated int
	// Unchanged is how many strongly matched containers had nothing to change.
	Unchanged int
	// NotFound is how many journals fatcat has no container for.
	NotFound int
	// Conflicted is how many journals matched a container by ISSN that carries
	// another journal's ISSN, and were left alone.
	Conflicted int
	// Unusable is how many records have no valid ISSN, or ISSNs from several
	// ISSN-L groups.
	Unusable int
	// Errored is how many journals failed and were passed over. Re-running
	// tries them again.
	Errored int
}

// BackfillOutput is where Backfill writes what it did, one JSON object per
// line. Either may be nil.
type BackfillOutput struct {
	// Updated gets each container as it is, or would be, after updating.
	Updated io.Writer
	// Conflicted gets each journal left alone for a conflicting match: its EZB
	// IDs, title and ISSNs.
	Conflicted io.Writer
}

// Backfill updates fatcat's containers from the EZB journal list in r, as JSON
// or TSV (see parseJournalsTSV). Unless
// apply is set it is a dry run: it makes only the container lookups and reads,
// and reports what it would write.
//
// Journals are processed by workers goroutines at once. One that fails is
// counted as errored and passed over. progress, if not nil, is called with the
// running tally every 1000 journals.
func Backfill(
	ctx context.Context,
	client *http.Client,
	r io.Reader,
	apply bool,
	workers int,
	out BackfillOutput,
	progress func(Counts),
) (Counts, error) {
	recs, err := parseJournalsAny(r)
	if err != nil {
		return Counts{}, err
	}
	rows := groupJournals(recs)
	if workers < 1 {
		workers = 1
	}

	var mu sync.Mutex
	var counts Counts
	var writeErr error
	write := func(w io.Writer, v any) {
		if w == nil || writeErr != nil {
			return
		}
		bs, err := json.Marshal(v)
		if err == nil {
			_, err = w.Write(append(bs, '\n'))
		}
		if err != nil {
			writeErr = fmt.Errorf("could not write backfill output: %w", err)
		}
	}

	next := make(chan journalRow)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range next {
				outcome, c, err := containers.UpdateExisting(client, row, !apply)

				mu.Lock()
				counts.Rows++
				switch {
				case err != nil:
					counts.Errored++
					log.Printf("error: %v %v", row.LogValues(), err)
				case outcome == containers.Updated:
					counts.Updated++
					write(out.Updated, c)
				case outcome == containers.Unchanged:
					counts.Unchanged++
				case outcome == containers.NotFound:
					counts.NotFound++
				case outcome == containers.Conflicted:
					counts.Conflicted++
					write(out.Conflicted, conflict(row))
				case outcome == containers.Unusable:
					counts.Unusable++
				}
				if progress != nil && counts.Rows%1000 == 0 {
					progress(counts)
				}
				mu.Unlock()
			}
		}()
	}

feed:
	for _, row := range rows {
		select {
		case <-ctx.Done():
			break feed
		case next <- row:
		}
	}
	close(next)
	wg.Wait()

	if writeErr != nil {
		return counts, writeErr
	}
	return counts, ctx.Err()
}

// conflictRecord is what a conflicted journal is written out as, for someone
// to look into by hand.
type conflictRecord struct {
	EZBIDs []int    `json:"ezb_ids"`
	Title  string   `json:"title"`
	ISSNL  string   `json:"issnl,omitempty"`
	ISSNs  []string `json:"issns"`
}

func conflict(row journalRow) conflictRecord {
	ids := []int{}
	for _, rec := range row.Records {
		ids = append(ids, rec.EZBID)
	}
	return conflictRecord{EZBIDs: ids, Title: row.title(), ISSNL: row.ISSNL, ISSNs: row.ISSNs}
}
