package etd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
)

// lineDeps is what processing one line needs, built once per run.
type lineDeps struct {
	FC        Fatcat
	Fetch     Fetcher
	Source    string
	Threshold float64
	DryRun    bool
	// Locks keeps concurrent lines that share an ID or a PDF from racing each
	// other between lookup and create. Nil locks nothing.
	Locks *keyedLocks
}

// lineResult is the verdict on one line. Job is set when the line's fatcat work
// is complete and it should be queued for indexing.
type lineResult struct {
	State   string
	Outcome Outcome
	Job     *IndexJob
}

// pdfRef is one distinct PDF (by sha1) a line points at. Several fulltext entries
// can be the same bytes -- whole and whole-verified, or two captures of one
// URL -- and they become one file with several URLs.
type pdfRef struct {
	sums Checksums
	// waybackURLs are the replay URLs to put on the file.
	waybackURLs []string
	// fetchURL is the raw-bytes URL to fetch it by again, for indexing.
	fetchURL string
	// existing is fc2's file with this sha1, if there is one.
	existing *fatcat2.File
}

// processLine takes one record through matching and fatcat writes.
//
// It returns an error only for something worth retrying -- a lookup or write
// that failed, a wayback fetch that timed out. Everything else, including the
// deliberate skips, is a verdict in lineResult.
func processLine(ctx context.Context, d lineDeps, hash string, rec Record) (lineResult, error) {
	oc := Outcome{Stage: "metadata", Key: rec.Key, LineHash: hash, Score: -1}
	verdict := func(state, status, detail string) (lineResult, error) {
		oc.Status, oc.Detail = status, detail
		return lineResult{State: state, Outcome: oc}, nil
	}

	if rec.Withdrawn != "" {
		return verdict(LineRejected, StatusReject, "withdrawn: "+rec.Withdrawn)
	}
	entries := rec.rankedFulltext()
	if len(entries) == 0 {
		return verdict(LineRejected, StatusReject, "no fulltext entries")
	}
	incoming := rec.ToRelease(d.Source)

	//
	// 1. external ID precheck
	//
	idKeys := make([]string, 0, len(incoming.ExternalIDs))
	for _, eid := range incoming.ExternalIDs {
		idKeys = append(idKeys, "id:"+eid.Type+":"+eid.Value)
	}
	defer d.Locks.lock(idKeys...)()

	var rid *uuid.UUID
	status := StatusCreated
	for _, eid := range incoming.ExternalIDs {
		found, err := d.FC.LookupRelease(eid.Type, eid.Value)
		if err != nil {
			return lineResult{}, fmt.Errorf("release lookup %s:%s: %w", eid.Type, eid.Value, err)
		}
		if found != nil {
			rid, status = found, StatusMatchID
			break
		}
	}

	// URLs already on the matched release's files, so that an entry whose
	// capture fatcat already has costs no wayback fetch.
	knownURL := map[string]*fatcat2.File{}
	if rid != nil {
		files, err := d.FC.ReleaseFiles(*rid)
		if err != nil {
			return lineResult{}, fmt.Errorf("files for release %s: %w", rid, err)
		}
		for i := range files {
			for _, u := range files[i].URLs {
				knownURL[u.URL] = &files[i]
			}
		}
	}

	//
	// 2. fetch and checksum each PDF, collapsing duplicates by sha1
	//
	var pdfs []*pdfRef
	bySha := map[string]*pdfRef{}
	var fetchFailures []string
	for _, e := range entries {
		var p *pdfRef
		if f, ok := knownURL[e.WaybackURL]; ok && e.Exact {
			p = &pdfRef{
				sums:        Checksums{Size: f.Size, Sha1: f.Sha1, Sha256: f.Sha256, Md5: f.Md5, WaybackURL: e.WaybackURL},
				waybackURLs: []string{e.WaybackURL},
				fetchURL:    e.FetchURL,
				existing:    f,
			}
		} else {
			sums, err := d.Fetch.Checksums(ctx, e.FetchURL)
			if isPermanent(err) {
				fetchFailures = append(fetchFailures, fmt.Sprintf("%s: %s", e.FetchURL, err))
				continue
			}
			if err != nil {
				return lineResult{}, err
			}
			if sums.WaybackURL == "" {
				sums.WaybackURL = e.WaybackURL
			}
			p = &pdfRef{sums: sums, waybackURLs: []string{sums.WaybackURL}, fetchURL: e.FetchURL}
		}
		if seen, ok := bySha[p.sums.Sha1]; ok {
			if !slices.Contains(seen.waybackURLs, p.sums.WaybackURL) {
				seen.waybackURLs = append(seen.waybackURLs, p.sums.WaybackURL)
			}
			if seen.existing == nil {
				seen.existing = p.existing
			}
			continue
		}
		bySha[p.sums.Sha1] = p
		pdfs = append(pdfs, p)
	}
	if len(pdfs) == 0 {
		return verdict(LineRejected, StatusReject, "no usable pdf: "+strings.Join(fetchFailures, "; "))
	}

	//
	// 3. sha1 lookup
	//
	shaKeys := make([]string, 0, len(pdfs))
	for _, p := range pdfs {
		shaKeys = append(shaKeys, "sha1:"+p.sums.Sha1)
	}
	defer d.Locks.lock(shaKeys...)()
	for _, p := range pdfs {
		if p.existing != nil {
			continue
		}
		fid, err := d.FC.LookupSha1(p.sums.Sha1)
		if err != nil {
			return lineResult{}, fmt.Errorf("sha1 lookup %s: %w", p.sums.Sha1, err)
		}
		if fid == nil {
			continue
		}
		f, err := d.FC.GetFile(*fid)
		if err != nil {
			return lineResult{}, fmt.Errorf("get file %s: %w", fid, err)
		}
		p.existing = &f
	}

	if rid == nil {
		matched, score, mismatch, err := matchBySha1(d, rec.Title, pdfs)
		if err != nil {
			return lineResult{}, err
		}
		oc.Score = score
		if mismatch != "" {
			// Decision 4: the PDF is in fatcat as something else. Write nothing
			// and leave the line for a human or a later run.
			return verdict(LineSkipped, StatusMismatch, mismatch)
		}
		if matched != nil {
			rid, status = matched, StatusMatchSha
		}
	}
	if rid == nil && strings.TrimSpace(rec.Title) == "" {
		return verdict(LineRejected, StatusReject, "no title, and nothing in fatcat to attach to")
	}

	//
	// 4. the release: fill an existing one's blanks, or create one
	//
	var rel fatcat2.Release
	var did []string
	if rid != nil {
		var err error
		rel, err = d.FC.GetRelease(*rid)
		if err != nil {
			return lineResult{}, fmt.Errorf("get release %s: %w", rid, err)
		}
		if filled := fillBlanks(&rel, incoming); len(filled) > 0 {
			if !d.DryRun {
				if err := d.FC.UpdateRelease(rel); err != nil {
					return lineResult{}, fmt.Errorf("update release %s: %w", rid, err)
				}
			}
			did = append(did, "filled release ("+strings.Join(filled, " ")+")")
		}
	} else {
		rel = incoming
		id, err := uuid.NewV7()
		if err != nil {
			return lineResult{}, err
		}
		rel.ID = id
		if !d.DryRun {
			created, err := d.FC.CreateRelease(rel)
			if err != nil {
				return lineResult{}, fmt.Errorf("create release: %w", err)
			}
			// fc2's create may adopt a legacy fatcat ident for the same IDs.
			rel.ID = created
		}
		did = append(did, "created release")
	}
	oc.ReleaseID = rel.ID.String()

	//
	// 5. the files
	//
	var fileIDs []string
	for _, p := range pdfs {
		fid, action, err := attachFile(d, rel, p)
		if err != nil {
			return lineResult{}, err
		}
		fileIDs = append(fileIDs, fid.String())
		if action != "" {
			did = append(did, action)
		}
	}
	oc.FileIDs = fileIDs

	detail := strings.Join(did, ", ")
	if detail == "" {
		detail = "already current"
	}
	if len(fetchFailures) > 0 {
		detail += fmt.Sprintf("; %d entries unusable: %s", len(fetchFailures), strings.Join(fetchFailures, "; "))
	}
	oc.Status, oc.Detail = status, detail

	return lineResult{
		State:   LineIndexing,
		Outcome: oc,
		Job: &IndexJob{
			LineHash:     hash,
			ReleaseID:    rel.ID.String(),
			FileIDs:      fileIDs,
			BestFileID:   fileIDs[0],
			BestFetchURL: pdfs[0].fetchURL,
		},
	}, nil
}

// matchBySha1 looks for a release to attach to through PDFs already in fatcat.
// A PDF whose file belongs to releases with a similar enough title names the
// match (the first such PDF wins, in best-first order). A PDF whose file
// belongs only to releases with dissimilar titles makes the whole line a
// mismatch, reported as a non-empty mismatch string. score is the best title
// similarity seen, or -1 if no comparison was made.
func matchBySha1(d lineDeps, title string, pdfs []*pdfRef) (match *uuid.UUID, score float64, mismatch string, err error) {
	score = -1
	var mismatches []string
	for _, p := range pdfs {
		if p.existing == nil {
			continue
		}
		rels := p.existing.Releases
		if len(rels) == 0 {
			// The embedded list can be trimmed (hidden releases); ask directly.
			rels, err = d.FC.FileReleases(p.existing.ID)
			if err != nil {
				return nil, score, "", fmt.Errorf("releases for file %s: %w", p.existing.ID, err)
			}
		}
		if len(rels) == 0 {
			continue // an orphan file: ours to claim
		}
		best, bestScore := rels[0], -1.0
		for _, r := range rels {
			if s := cleaning.TitleSimilarity(title, r.Title); s > bestScore {
				best, bestScore = r, s
			}
		}
		score = max(score, bestScore)
		if bestScore >= d.Threshold {
			if match == nil {
				id := best.ID
				match = &id
			}
			continue
		}
		for _, r := range rels {
			mismatches = append(mismatches, fmt.Sprintf("file %s sha1 %s -> release %s %q (%.3f)",
				p.existing.ID, p.sums.Sha1, r.ID, r.Title, cleaning.TitleSimilarity(title, r.Title)))
		}
	}
	if len(mismatches) > 0 {
		return nil, score, fmt.Sprintf("ours %q vs %s", title, strings.Join(mismatches, "; ")), nil
	}
	return match, score, "", nil
}

// attachFile makes sure p is a file in fc2 that carries its wayback URLs and is
// linked to rel, creating or updating as needed. It returns the file's ID and a
// short note of what it did, empty if nothing.
func attachFile(d lineDeps, rel fatcat2.Release, p *pdfRef) (uuid.UUID, string, error) {
	if p.existing == nil {
		fid, err := uuid.NewV7()
		if err != nil {
			return uuid.Nil, "", err
		}
		f := fatcat2.File{
			ID:       fid,
			Releases: []fatcat2.Release{rel},
			Source:   d.Source,
			Size:     p.sums.Size,
			Sha1:     p.sums.Sha1,
			Sha256:   p.sums.Sha256,
			Md5:      p.sums.Md5,
			Mimetype: "application/pdf",
		}
		for _, u := range p.waybackURLs {
			f.URLs = append(f.URLs, fatcat2.FileURL{FileID: fid, Rel: "webarchive", URL: u})
		}
		if !d.DryRun {
			created, err := d.FC.CreateFile(f)
			if err != nil {
				return uuid.Nil, "", fmt.Errorf("create file %s: %w", p.sums.Sha1, err)
			}
			fid = created
		}
		return fid, "created file", nil
	}

	f := *p.existing
	linked := slices.ContainsFunc(f.Releases, func(r fatcat2.Release) bool { return r.ID == rel.ID })
	var newURLs []string
	for _, u := range p.waybackURLs {
		if !slices.ContainsFunc(f.URLs, func(fu fatcat2.FileURL) bool { return fu.URL == u }) {
			newURLs = append(newURLs, u)
		}
	}

	if linked {
		// URLs only: the per-URL endpoint is idempotent and touches nothing
		// else on the file.
		for _, u := range newURLs {
			if !d.DryRun {
				if _, err := d.FC.AddFileURL(f.ID, fatcat2.FileURL{Rel: "webarchive", URL: u}); err != nil {
					return uuid.Nil, "", fmt.Errorf("add url to file %s: %w", f.ID, err)
				}
			}
		}
		if len(newURLs) > 0 {
			return f.ID, "added url", nil
		}
		return f.ID, "", nil
	}

	// Linking a release needs a whole-file PUT. Re-read the file first so the
	// PUT is built from its current state rather than from whatever it was
	// when this line started.
	if !d.DryRun {
		fresh, err := d.FC.GetFile(f.ID)
		if err != nil {
			return uuid.Nil, "", fmt.Errorf("re-read file %s: %w", f.ID, err)
		}
		f = fresh
	}
	f.Releases = append(f.Releases, rel)
	for _, u := range newURLs {
		if !slices.ContainsFunc(f.URLs, func(fu fatcat2.FileURL) bool { return fu.URL == u }) {
			f.URLs = append(f.URLs, fatcat2.FileURL{FileID: f.ID, Rel: "webarchive", URL: u})
		}
	}
	if !d.DryRun {
		if err := d.FC.UpdateFile(f); err != nil {
			return uuid.Nil, "", fmt.Errorf("link file %s to release %s: %w", f.ID, rel.ID, err)
		}
	}
	return f.ID, "linked file", nil
}
