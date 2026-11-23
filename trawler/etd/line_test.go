package etd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFatcat is an in-memory fc2 with the semantics processLine relies on.
type fakeFatcat struct {
	mu       sync.Mutex
	releases map[uuid.UUID]fatcat2.Release
	files    map[uuid.UUID]fatcat2.File
	writes   []string
	failNext error
}

func newFakeFatcat() *fakeFatcat {
	return &fakeFatcat{releases: map[uuid.UUID]fatcat2.Release{}, files: map[uuid.UUID]fatcat2.File{}}
}

func (f *fakeFatcat) addRelease(r fatcat2.Release) fatcat2.Release {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	w := uuid.New()
	r.WorkID = &w
	f.releases[r.ID] = r
	return r
}

func (f *fakeFatcat) addFile(fl fatcat2.File) fatcat2.File {
	if fl.ID == uuid.Nil {
		fl.ID = uuid.New()
	}
	f.files[fl.ID] = fl
	return fl
}

func (f *fakeFatcat) err() error {
	err := f.failNext
	f.failNext = nil
	return err
}

func (f *fakeFatcat) LookupRelease(t, v string) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return nil, err
	}
	for id, r := range f.releases {
		for _, e := range r.ExternalIDs {
			if e.Type == t && e.Value == v {
				return &id, nil
			}
		}
	}
	return nil, nil
}
func (f *fakeFatcat) GetRelease(id uuid.UUID) (fatcat2.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.releases[id]
	if !ok {
		return r, fmt.Errorf("no release %s", id)
	}
	return r, nil
}
func (f *fakeFatcat) ReleaseFiles(id uuid.UUID) ([]fatcat2.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fatcat2.File
	for _, fl := range f.files {
		if slices.ContainsFunc(fl.Releases, func(r fatcat2.Release) bool { return r.ID == id }) {
			out = append(out, fl)
		}
	}
	return out, nil
}
func (f *fakeFatcat) CreateRelease(r fatcat2.Release) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := uuid.New()
	r.WorkID = &w
	f.releases[r.ID] = r
	f.writes = append(f.writes, "create release")
	return r.ID, nil
}
func (f *fakeFatcat) UpdateRelease(r fatcat2.Release) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases[r.ID] = r
	f.writes = append(f.writes, "update release")
	return nil
}
func (f *fakeFatcat) LookupSha1(s string) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, fl := range f.files {
		if fl.Sha1 == s {
			return &id, nil
		}
	}
	return nil, nil
}
func (f *fakeFatcat) GetFile(id uuid.UUID) (fatcat2.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.files[id]
	if !ok {
		return fl, fmt.Errorf("no file %s", id)
	}
	fl.URLs = slices.Clone(fl.URLs)
	fl.Releases = slices.Clone(fl.Releases)
	return fl, nil
}
func (f *fakeFatcat) FileReleases(id uuid.UUID) ([]fatcat2.Release, error) {
	fl, err := f.GetFile(id)
	return fl.Releases, err
}
func (f *fakeFatcat) CreateFile(fl fatcat2.File) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fl.ID] = fl
	f.writes = append(f.writes, "create file")
	return fl.ID, nil
}
func (f *fakeFatcat) UpdateFile(fl fatcat2.File) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fl.ID] = fl
	f.writes = append(f.writes, "update file")
	return nil
}
func (f *fakeFatcat) AddFileURL(id uuid.UUID, u fatcat2.FileURL) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.files[id]
	fl.URLs = append(fl.URLs, fatcat2.FileURL{FileID: id, Rel: u.Rel, URL: u.URL})
	f.files[id] = fl
	f.writes = append(f.writes, "add url")
	return true, nil
}

// fakeFetcher serves PDFs by fetch URL; a URL with no entry is a 404.
type fakeFetcher struct {
	mu      sync.Mutex
	pdfs    map[string]Checksums
	errs    map[string]error
	fetched []string
}

func (f *fakeFetcher) Checksums(_ context.Context, u string) (Checksums, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, u)
	if err, ok := f.errs[u]; ok {
		return Checksums{}, err
	}
	c, ok := f.pdfs[u]
	if !ok {
		return Checksums{}, &PermanentFetchError{Reason: "wayback returned 404"}
	}
	return c, nil
}
func (f *fakeFetcher) Bytes(_ context.Context, u string) ([]byte, error) {
	return []byte("%PDF-1.4 " + u), nil
}

func sums(sha1, wayback string) Checksums {
	return Checksums{Size: 10, Sha1: sha1, Sha256: sha1 + "-256", Md5: sha1 + "-md5", WaybackURL: wayback}
}

func testDeps(fc *fakeFatcat, fe *fakeFetcher) lineDeps {
	return lineDeps{FC: fc, Fetch: fe, Source: "etd-test", Threshold: 0.85}
}

func TestProcessLine_CreatesReleaseAndFile(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[0]
	fc, fe := newFakeFatcat(), &fakeFetcher{pdfs: map[string]Checksums{
		rec.Fulltext[0].FetchURL: sums("aaa", rec.Fulltext[0].WaybackURL),
	}}

	res, err := processLine(context.Background(), testDeps(fc, fe), "h0", rec)
	require.NoError(t, err)
	assert.Equal(t, LineIndexing, res.State)
	assert.Equal(t, StatusCreated, res.Outcome.Status)
	assert.Equal(t, []string{"create release", "create file"}, fc.writes)

	require.NotNil(t, res.Job)
	rid := uuid.MustParse(res.Job.ReleaseID)
	rel := fc.releases[rid]
	assert.Equal(t, "etd-test", rel.Source)
	assert.Equal(t, "thesis", rel.Type)

	fid := uuid.MustParse(res.Job.BestFileID)
	fl := fc.files[fid]
	assert.Equal(t, "etd-test", fl.Source)
	assert.Equal(t, "aaa", fl.Sha1)
	assert.Equal(t, []fatcat2.FileURL{{FileID: fid, Rel: "webarchive", URL: rec.Fulltext[0].WaybackURL}}, fl.URLs)
	assert.Equal(t, rid, fl.Releases[0].ID)
	assert.Equal(t, rec.Fulltext[0].FetchURL, res.Job.BestFetchURL)
}

func TestProcessLine_IDMatchFillsBlanksAndSkipsKnownCapture(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[0]
	fc, fe := newFakeFatcat(), &fakeFetcher{}
	rel := fc.addRelease(fatcat2.Release{
		Title:       "Whatever fatcat calls it",
		ExternalIDs: []fatcat2.ExternalID{{Type: "hdl", Value: "10554/1457"}},
	})
	fl := fc.addFile(fatcat2.File{
		Sha1:     "known",
		Releases: []fatcat2.Release{rel},
		URLs:     []fatcat2.FileURL{{Rel: "webarchive", URL: rec.Fulltext[0].WaybackURL}},
	})

	res, err := processLine(context.Background(), testDeps(fc, fe), "h0", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusMatchID, res.Outcome.Status)
	assert.Empty(t, fe.fetched, "a capture fatcat already has costs no fetch")
	assert.Equal(t, []string{"update release"}, fc.writes)

	got := fc.releases[rel.ID]
	assert.Equal(t, "Whatever fatcat calls it", got.Title)
	assert.Equal(t, "es", got.Language)
	assert.Contains(t, got.ExternalIDs, fatcat2.ExternalID{Type: "doi", Value: "10.11144/javeriana.10554.1457"})
	assert.Equal(t, []string{fl.ID.String()}, res.Job.FileIDs)
}

func TestProcessLine_IDMatchLinksExistingFileAndAddsURL(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[0]
	fc := newFakeFatcat()
	rel := fc.addRelease(fatcat2.Release{Title: "x", ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.11144/javeriana.10554.1457"}}})
	other := fc.addRelease(fatcat2.Release{Title: "some preprint"})
	fl := fc.addFile(fatcat2.File{Sha1: "aaa", Releases: []fatcat2.Release{other},
		URLs: []fatcat2.FileURL{{Rel: "web", URL: "http://example.org/a.pdf"}}})
	fe := &fakeFetcher{pdfs: map[string]Checksums{rec.Fulltext[0].FetchURL: sums("aaa", rec.Fulltext[0].WaybackURL)}}

	res, err := processLine(context.Background(), testDeps(fc, fe), "h0", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusMatchID, res.Outcome.Status)
	assert.Contains(t, fc.writes, "update file")

	got := fc.files[fl.ID]
	assert.ElementsMatch(t, []uuid.UUID{other.ID, rel.ID}, []uuid.UUID{got.Releases[0].ID, got.Releases[1].ID})
	assert.Contains(t, got.URLs, fatcat2.FileURL{FileID: fl.ID, Rel: "webarchive", URL: rec.Fulltext[0].WaybackURL})
	assert.Contains(t, got.URLs, fatcat2.FileURL{Rel: "web", URL: "http://example.org/a.pdf"})
}

func TestProcessLine_Sha1MatchWithAgreeingTitle(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[2] // no ID in fatcat; three PDFs
	fc := newFakeFatcat()
	rel := fc.addRelease(fatcat2.Release{Title: rec.Title + "."})
	fe := &fakeFetcher{pdfs: map[string]Checksums{}}
	for i, f := range rec.Fulltext {
		fe.pdfs[f.FetchURL] = sums(fmt.Sprintf("sha%d", i), fmt.Sprintf("https://web.archive.org/web/2020010100000%d/x", i))
	}
	fl := fc.addFile(fatcat2.File{Sha1: "sha2", Releases: []fatcat2.Release{rel}})

	res, err := processLine(context.Background(), testDeps(fc, fe), "h2", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusMatchSha, res.Outcome.Status)
	assert.Equal(t, rel.ID.String(), res.Job.ReleaseID)
	assert.InDelta(t, 1.0, res.Outcome.Score, 0.001)
	assert.Len(t, res.Job.FileIDs, 3)
	assert.Contains(t, res.Job.FileIDs, fl.ID.String())
	assert.NotContains(t, fc.writes, "create release")
	// The exact capture ranks first, so it is the one indexed.
	assert.Equal(t, rec.Fulltext[2].FetchURL, res.Job.BestFetchURL)
}

func TestProcessLine_Sha1TitleMismatchWritesNothing(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[2]
	fc := newFakeFatcat()
	rel := fc.addRelease(fatcat2.Release{Title: "A Completely Different Dissertation About Fish"})
	fc.addFile(fatcat2.File{Sha1: "sha1", Releases: []fatcat2.Release{rel}})
	fe := &fakeFetcher{pdfs: map[string]Checksums{}}
	for i, f := range rec.Fulltext {
		fe.pdfs[f.FetchURL] = sums(fmt.Sprintf("sha%d", i), "")
	}

	res, err := processLine(context.Background(), testDeps(fc, fe), "h2", rec)
	require.NoError(t, err)
	assert.Equal(t, LineSkipped, res.State)
	assert.Equal(t, StatusMismatch, res.Outcome.Status)
	assert.Nil(t, res.Job)
	assert.Empty(t, fc.writes, "a mismatch writes nothing, even for the line's new PDFs")
	assert.Contains(t, res.Outcome.Detail, rel.ID.String())
	assert.Less(t, res.Outcome.Score, 0.85)
}

func TestProcessLine_DuplicatePDFsBecomeOneFile(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[2]
	fc := newFakeFatcat()
	fe := &fakeFetcher{pdfs: map[string]Checksums{}}
	for i, f := range rec.Fulltext {
		fe.pdfs[f.FetchURL] = sums("same", fmt.Sprintf("https://web.archive.org/web/2020010100000%d/x", i))
	}

	res, err := processLine(context.Background(), testDeps(fc, fe), "h2", rec)
	require.NoError(t, err)
	require.Len(t, res.Job.FileIDs, 1)
	assert.Len(t, fc.files[uuid.MustParse(res.Job.FileIDs[0])].URLs, 3)
}

func TestProcessLine_UnusablePDFs(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[2]
	fc := newFakeFatcat()

	// Some entries 404: the rest are ingested and the detail says so.
	fe := &fakeFetcher{pdfs: map[string]Checksums{rec.Fulltext[2].FetchURL: sums("ok", "https://web.archive.org/web/20200101000000/x")}}
	res, err := processLine(context.Background(), testDeps(fc, fe), "h2", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, res.Outcome.Status)
	assert.Contains(t, res.Outcome.Detail, "2 entries unusable")

	// All of them: rejected, nothing written.
	fc = newFakeFatcat()
	res, err = processLine(context.Background(), testDeps(fc, &fakeFetcher{}), "h2", rec)
	require.NoError(t, err)
	assert.Equal(t, LineRejected, res.State)
	assert.Empty(t, fc.writes)
}

func TestProcessLine_TransientErrorsAreErrors(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[0]
	fc := newFakeFatcat()
	fe := &fakeFetcher{errs: map[string]error{rec.Fulltext[0].FetchURL: errors.New("wayback returned 503")}}
	_, err := processLine(context.Background(), testDeps(fc, fe), "h0", rec)
	assert.Error(t, err)

	fc.failNext = errors.New("fc2 is down")
	_, err = processLine(context.Background(), testDeps(fc, &fakeFetcher{}), "h0", rec)
	assert.ErrorContains(t, err, "fc2 is down")
	assert.Empty(t, fc.writes)
}

func TestProcessLine_Rejections(t *testing.T) {
	recs, _ := sampleRecords(t)
	fc := newFakeFatcat()

	res, err := processLine(context.Background(), testDeps(fc, &fakeFetcher{}), "h3", recs[3])
	require.NoError(t, err)
	assert.Equal(t, LineRejected, res.State)
	assert.Contains(t, res.Outcome.Detail, "withdrawn")

	// No title and nothing in fatcat to attach to.
	rec := recs[1]
	fe := &fakeFetcher{pdfs: map[string]Checksums{rec.Fulltext[0].FetchURL: sums("t", "")}}
	res, err = processLine(context.Background(), testDeps(fc, fe), "h1", rec)
	require.NoError(t, err)
	assert.Equal(t, LineRejected, res.State)
	assert.Contains(t, res.Outcome.Detail, "no title")
	assert.Empty(t, fc.writes)

	// Same untitled record, but its DOI is in fatcat: fine.
	fc.addRelease(fatcat2.Release{Title: "Zur Theorie der Abbildungen", ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.1007/bf01702717"}}})
	res, err = processLine(context.Background(), testDeps(fc, fe), "h1", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusMatchID, res.Outcome.Status)
}

func TestProcessLine_DryRunWritesNothing(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[0]
	fc := newFakeFatcat()
	fe := &fakeFetcher{pdfs: map[string]Checksums{rec.Fulltext[0].FetchURL: sums("aaa", "")}}
	d := testDeps(fc, fe)
	d.DryRun = true

	res, err := processLine(context.Background(), d, "h0", rec)
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, res.Outcome.Status)
	assert.Empty(t, fc.writes)
	assert.Empty(t, fc.releases)
}
