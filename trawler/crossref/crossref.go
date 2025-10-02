package crossref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"git.archive.org/webgroup/scholar/trawler/issn"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// scrape crossref for a day's worth of metadata: huge ndjson file in s3, each line a paper-like entity
// for each entity, create an entry in fatcat2
// for each entity, if there's a suitable URL, try and obtain a PDF
// for each obtained PDF
// - extract metadata and make sure it matches the fatcat2 record
// - create a file entry in fatcat2
// - extract fulltext and ingest into elasticsearch

// containerTypeMap maps from fatcat release types to their assumed parent container type
var containerTypeMap = map[string]string{
	"article-journal":  "journal",
	"paper-conference": "conference",
	"book":             "book-series",
}

var releaseTypeMap = map[string]string{
	// CSL types
	"book":                "book",
	"book-chapter":        "chapter",
	"book-part":           "chapter",
	"book-section":        "chapter",
	"dataset":             "dataset",
	"dissertation":        "thesis",
	"edited-book":         "book",
	"journal-article":     "article-journal",
	"monograph":           "book",
	"posted-content":      "post",
	"proceedings-article": "paper-conference",
	"report":              "report",

	// considering switching to ignored types pending Jefferson convo/research

	// looking at releases with no types and "crossref" in the extra_json in
	// fatcat1, this seems to often describe figures
	// TODO waiting on jefferson's call
	"other":           "",
	"reference-book":  "book",
	"reference-entry": "entry",
	"standard":        "standard",

	// non-CSL types
	"component": "component",
}

type Abstract struct {
	// TODO
}

type ReleaseContrib struct {
	// TODO
}

type ExternalID struct {
	Type  string `json:"id_type"`
	Value string `json:"id_value"`
}
type Container struct {
	ID          uuid.UUID      `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Type        string         `json:"container_type,omitempty"`
	LegacyRevID uuid.UUID      `json:"legacy_rev_id,omitempty"`
	Publisher   string         `json:"publisher,omitempty"`
	ISSNL       string         `json:"issnl,omitempty"`
	ISSNE       string         `json:"issne,omitempty"`
	ISSNP       string         `json:"issnp,omitempty"`
	Source      string         `json:"source,omitempty"`
	Extra       map[string]any `json:"extra"`
}

// ReleaseRef is a link from a citing release to a cited release
type ReleaseRef struct {
	// TODO
}

// TODO split out into its own package
type Release struct {
	Title         string
	OriginalTitle string `json:"original_title"`
	Subtitle      string
	Type          string    `json:"release_type"`
	Stage         string    `json:"release_stage"`
	ReleaseDate   time.Time `json:"release_date"`
	ReleaseYear   int       `json:"release_year"`
	Volume        string
	Issue         string
	Pages         string
	Language      string
	LicenseSlug   string `json:"license_slug"`
	Extra         map[string]any

	// Foreign keys

	Refs        []RawRef
	Abstracts   []Abstract
	Citations   []ReleaseRef
	ContainerID uuid.UUID `json:"container_id"`
	ExternalIDs []ExternalID
	Contribs    []ReleaseContrib

	// unused in xref but may want later:
	// Pages string
	// WithdrawnStatus string
}

// TODO abstracts
// TODO refs
// TODO extra
// TODO ext ids
// TODO contribs

// RawRef is stored in fatcat2's database as a json value in a release row
type RawRef struct {
	// TODO I don't like how this is structured (wayyy too much shoved in extra)
	// but just maintaining parity for now with legacy fatcat

	// NB no indication TargetReleaseID is ever set
	Title           string         `json:"title,omitempty"`
	TargetReleaseID uuid.UUID      `json:"target_release_id,omitempty"`
	Index           int            `json:"index,omitempty"`
	Key             string         `json:"key,omitempty"`
	Year            int            `json:"year,omitempty"`
	ContainerName   string         `json:"container_name,omitempty"`
	Locator         string         `json:"locator,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

// TODO crossref structs
type crossrefRef struct {
	Year            string
	Key             string
	JournalTitle    string `json:"journal-title"`
	VolumeTitle     string `json:"volume-title"`
	DOI             string
	Author          string
	Editor          string
	Edition         string
	Authority       string
	Version         string
	Genre           string
	URL             string `json:"url"`
	Event           string
	ArticleTitle    string `json:"article-title"`
	FirstPage       string `json:"first-page"`
	Issue           string
	Volume          string
	Date            string
	AccessedDate    string `json:"accessed_date"`
	Issued          string
	Page            string
	Medium          string
	CollectionTitle string `json:"collection_title"`
	ChapterNumber   string `json:"chapter_number"`
	Unstructured    string
	SeriesTitle     string `json:"series-title"`
}
type crossrefLicense struct {
	URL            string
	ContentVersion string `json:"content-version"`
}

type crossrefDoc struct {
	ContainerTitle []string `json:"container-title"`
	DOI            string
	ISSN           []string
	License        []crossrefLicense
	Reference      []crossrefRef
	Publisher      string
	Title          []string
	Type           string
}

var ignoredTypes = []string{
	"",
	"journal",
	"proceedings",
	"standard-series",
	"report-series",
	"book-series",
	"book-set",
	"book-track",
	"proceedings-series",
	"peer-review",
}

func (c crossrefDoc) IsSkippable() bool {
	if c.Title == nil {
		return true
	}

	if len(c.Title) < 1 {
		return true
	}

	if strings.ToLower(c.Title[0]) == "oup accepted manuscript" {
		return true
	}

	if slices.Contains(ignoredTypes, c.Type) {
		return true
	}

	if c.DOI == "" {
		return true
	}

	return false
}

/*
type CrossrefCrawlResult struct {
	FoundCounts struct {
		Releases   int
		Containers int
		Creators   int
	}
	CreatedCounts struct {
		Releases   int
		Containers int
		Creators   int
	}
	PDFCount      int
	IngestedCount int
}
*/

type CrossrefCrawlInput struct {
	SKInput SKCrossrefInput
}

type releaseCounts struct {
	// Skipped is the count of lines in the upstream we knew we would never want
	Skipped int
	// Ignored is the count of lines in the upstream metadata we were already aware of
	Ignored int
	// Added is the count of lines from the upstream metadata we added to Fatcat
	Added int
	// Acquired is the count of PDFs we acquired from the upstream metadata
	Acquired int
}

type containerCounts struct {
	// Ignored is the count of containers fatcat already knew about
	Ignored int
	// Added is the count of containers we created in fatcat
	Added int
	// Skipped is the count of containers we did not create because of data issues
	Skipped int
}

type counts struct {
	Releases   releaseCounts
	Containers containerCounts
}

func (c counts) Add(other counts) counts {
	return counts{
		Releases: releaseCounts{
			Skipped:  c.Releases.Skipped + other.Releases.Skipped,
			Ignored:  c.Releases.Ignored + other.Releases.Ignored,
			Added:    c.Releases.Added + other.Releases.Added,
			Acquired: c.Releases.Acquired + other.Releases.Acquired,
		},
		Containers: containerCounts{
			Ignored: c.Containers.Ignored + other.Containers.Ignored,
			Added:   c.Containers.Added + other.Containers.Added,
		},
	}
}

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
}

func lineBatchWorkflow(ctx workflow.Context, in lineBatchInput) (counts, error) {
	out := counts{}
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,                       // TODO tune, config maybe
		TaskQueue:           viper.GetString("crossref.internal_task_queue"), // TODO needed?
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	lin := lineInput{
		S3Key: in.S3Key,
	}
	for _, offset := range in.Offsets {
		lin.LineStart = offset[0]
		lin.Length = offset[1]

		var c counts

		err := workflow.ExecuteActivity(ctx, processLine, lin).Get(ctx, &c)
		if err != nil {
			return out, err
		}
		out = out.Add(c)
	}
	return out, nil
}

type lineInput struct {
	S3Key     string
	LineStart int64
	Length    int64
}

// TODO split this up into smaller activities
func processLine(ctx context.Context, in lineInput) (out counts, err error) {
	out = counts{}
	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	lineb := make([]byte, in.Length)
	n, err := f.ReadAt(lineb, in.LineStart)
	if err != nil {
		return
	}
	if n == 0 {
		return out, fmt.Errorf("read 0 bytes, expected %d", len(lineb))
	}

	l := activity.GetLogger(ctx)
	//l.Debug(string(lineb))

	var record crossrefDoc

	err = json.Unmarshal(lineb, &record)
	if err != nil {
		return
	}

	l.Info(fmt.Sprintf("got a '%s' with doi '%s'", record.Type, record.DOI))

	// Should we skip even checking the DOI?

	if record.IsSkippable() {
		out.Releases.Skipped++
		return out, nil
	}

	// Check the DOI

	client := &http.Client{}
	found, err := isExistingDOI(client, record.DOI)
	if err != nil {
		return out, err
	}

	if found {
		out.Releases.Ignored++
		return out, nil
	}

	// if things get weird we'll put some stuff in here
	extra := map[string]any{}
	var releaseType string
	releaseType, ok := releaseTypeMap[record.Type]
	if !ok {
		return out, fmt.Errorf("found unknown crossref type '%s'", record.Type)
	}

	var containerTitle string

	if len(record.ContainerTitle) > 0 {
		// TODO fatcat importer is using ftfy to clean this value up; we can do
		// that on the server side on container creation.
		// TODO fatcat importer was arbitrarily using the first container title in
		// the list so I've continued that practice but it feels weird
		containerTitle = record.ContainerTitle[0]
	}

	var issnl string
	for _, i := range record.ISSN {
		issnl = issn.ISSN2ISSNL(i)
		if issnl != "" {
			break
		}
	}

	containerID := uuid.Nil

	if issnl != "" {
		// TODO could build a map of issnl->cid somewhere to save on requests
		containerID, err = lookupContainer(client, issnl)
		if err != nil {
			return out, err
		}
	}

	if containerID == uuid.Nil {
		if containerTitle != "" && issnl != "" {
			c := Container{
				Name:      containerTitle,
				ISSNL:     issnl,
				Publisher: record.Publisher,
				Type:      containerTypeMap[releaseType],
			}
			containerID, err = createContainer(client, c)
			if err != nil {
				return out, err
			}
			out.Containers.Added++
		} else if containerTitle != "" {
			extra["container_name"] = containerTitle
			out.Containers.Skipped++
		}
	} else {
		out.Containers.Ignored++
	}

	r := Release{
		ContainerID: containerID,
		// TODO fill in other stuff
	}

	// licenses

	for _, lic := range record.License {
		// the original fatcat code iterated over every license running code like
		// this; that means it would only ever take the last license in a list of
		// licenses. i've preserved that side effect here.
		if lic.ContentVersion != "vor" && lic.ContentVersion != "unspecified" {
			continue
		}
		r.LicenseSlug = licenseSlugLookup(lic.URL)
	}

	// references
	r.Refs = []RawRef{}

	for i, cref := range record.Reference {
		rawRef := RawRef{
			Index:   i,
			Locator: cref.FirstPage,
			Title:   cref.ArticleTitle,
			Extra:   map[string]any{},
		}

		year, err := strconv.Atoi(cref.Year)
		if err == nil {
			rawRef.Year = year
		}

		if cref.Key != "" {
			key := strings.TrimPrefix(cref.Key, strings.ToUpper(record.DOI)+"-")
			key = strings.TrimPrefix(cref.Key, strings.ToUpper(record.DOI))
			rawRef.Key = key
		}

		rawRef.ContainerName = cref.VolumeTitle
		if rawRef.ContainerName == "" {
			rawRef.ContainerName = cref.JournalTitle
		}

		// "extra" stuff (i hate this)

		if cref.JournalTitle != "" {
			rawRef.Extra["journal-title"] = cref.JournalTitle
		}

		if cref.DOI != "" {
			rawRef.Extra["DOI"] = cref.DOI
		}

		if cref.Author != "" {
			// why is this a list?
			rawRef.Extra["authors"] = []string{cref.Author}
		}

		if cref.Editor != "" {
			rawRef.Extra["editor"] = cref.Editor
		}
		if cref.Edition != "" {
			rawRef.Extra["edition"] = cref.Edition
		}
		if cref.Authority != "" {
			rawRef.Extra["authority"] = cref.Authority
		}
		if cref.Version != "" {
			rawRef.Extra["version"] = cref.Version
		}
		if cref.Genre != "" {
			rawRef.Extra["genre"] = cref.Genre
		}
		if cref.URL != "" {
			rawRef.Extra["url"] = cref.URL
		}
		if cref.Event != "" {
			rawRef.Extra["event"] = cref.Event
		}
		if cref.Issue != "" {
			rawRef.Extra["issue"] = cref.Issue
		}
		if cref.Volume != "" {
			rawRef.Extra["volume"] = cref.Volume
		}
		if cref.Date != "" {
			rawRef.Extra["date"] = cref.Date
		}
		if cref.AccessedDate != "" {
			rawRef.Extra["accessed_date"] = cref.AccessedDate
		}
		if cref.Issue != "" {
			rawRef.Extra["issue"] = cref.Issue
		}
		if cref.Page != "" {
			rawRef.Extra["page"] = cref.Page
		}
		if cref.Medium != "" {
			rawRef.Extra["medium"] = cref.Medium
		}
		if cref.CollectionTitle != "" {
			rawRef.Extra["collection_title"] = cref.CollectionTitle
		}
		if cref.ChapterNumber != "" {
			rawRef.Extra["chapter_number"] = cref.ChapterNumber
		}
		if cref.Unstructured != "" {
			rawRef.Extra["unstructured"] = cref.Unstructured
		}
		if cref.SeriesTitle != "" {
			rawRef.Extra["series-title"] = cref.SeriesTitle
		}
		if cref.VolumeTitle != "" {
			rawRef.Extra["volume-title"] = cref.VolumeTitle
		}

		r.Refs = append(r.Refs, rawRef)

	}

	fmt.Println(r)

	// TODO create entity for insertion
	// TODO insert into db (Added++)
	// TODO wait for spn slot
	// TODO submit to spn
	// TODO fetch pdf from wayback
	// TODO store pdf in seaweed
	// TODO extract and check PDF metadata
	// TODO insert file into db
	// TODO ingest PDF (Acquired++)
	return out, nil
}

func licenseSlugLookup(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	rawURL = strings.ToLower(rawURL)
	rawURL = strings.TrimSuffix(rawURL, "/")
	rawURL = strings.ReplaceAll(rawURL, "https://", "//")
	rawURL = strings.ReplaceAll(rawURL, "http://", "//")
	if strings.Contains(rawURL, "creativecommons.org") {
		rawURL = strings.ReplaceAll(rawURL, "/legalcode", "")
		rawURL = strings.ReplaceAll(rawURL, "/uk", "")
	}
	return licenseSlugMap[rawURL]
}

const contQ = `
SELECT
  ident.id,
	ident.rev_id AS legacy_rev_id,
	rev.issne,
	rev.issnp,
	rev.extra_json AS extra
FROM container_ident ident
JOIN container_rev rev ON rev.id = ident.rev_id
WHERE rev.issnl = $1
LIMIT 1;
`

// createContainer creates a new container in fc2 and returns its ID
func createContainer(client *http.Client, c Container) (uuid.UUID, error) {
	c.Source = "dev" // TODO thread this value through from invocation of workflow
	c.ID = uuid.New()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, viper.GetString("fatcat1.pgurl"))
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to connect to legacy fatcat: %w", err)
	}

	err = conn.QueryRow(ctx, contQ, c.ISSNL).Scan(
		&c.ID,
		&c.LegacyRevID,
		&c.ISSNE,
		&c.ISSNP,
		&c.Extra,
	)

	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("failed to talk to old fatcat db: %w", err)
	}

	// if found, insert that data and return the ID
	fc2url := viper.GetString("fatcat2.endpoint")
	fc2key := viper.GetString("fatcat2.key")

	bs, err := json.Marshal(c)

	body := bytes.NewBuffer(bs)
	req, err := http.NewRequest("POST", fc2url+"/container", body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", fc2key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("container POST failed for '%#v': %w", c, err)
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, fmt.Errorf("unexpected status code for '%#v' POST: %d; body '%s'", c, resp.StatusCode, b)
	}

	return c.ID, nil
}

// TODO make a client wrapper
//type FC2Client http.Client

func lookupContainer(c *http.Client, issnl string) (uuid.UUID, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/container/lookup", nil)
	if err != nil {
		panic(err)
	}

	q := req.URL.Query()
	q.Add("id_type", "issnl")
	q.Add("id_value", issnl)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fc2 lookup failed for '%s': %w", issnl, err)
	}

	if resp.StatusCode == 404 {
		return uuid.Nil, nil
	}

	if resp.StatusCode != 200 {
		return uuid.Nil, fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			issnl, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return uuid.Nil, err
	}

	var p struct {
		ID uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unmarshal failed for '%s': %w", issnl, err)
	}

	return p.ID, nil
}

func isExistingDOI(c *http.Client, doi string) (bool, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/release/lookup", nil)
	if err != nil {
		panic(err)
	}
	q := req.URL.Query()
	q.Add("id_type", "doi")
	q.Add("id_value", doi)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return false, fmt.Errorf("fc2 lookup failed for '%s': %w", doi, err)
	}

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}

	if resp.StatusCode != 404 {
		err = fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			doi, resp.StatusCode)
	}

	return false, err
}

type findLineBatchInput struct {
	S3Key  string
	Offset int64
}

type findLineBatchOutput struct {
	Offsets   [][]int64
	BytesRead int64
	EOF       bool
}

func findLineBatch(ctx context.Context, in findLineBatchInput) (out findLineBatchOutput, err error) {
	out = findLineBatchOutput{}

	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	batchSize := 1000 // TODO set in config

	// TODO refactor this so it's unit testable
	chunkSize := 1024 * 100 // TODO set in config
	out.BytesRead = in.Offset
	curLineStart := in.Offset

	var done bool
	var curLineLength int64

	for !done {
		b := make([]byte, chunkSize)
		n, err := f.ReadAt(b, out.BytesRead)
		l.Debug(fmt.Sprintf("read %d bytes", n))
		if errors.Is(err, io.EOF) {
			l.Debug("saw EOF")
			out.EOF = true
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read of '%s' failed: %w", in.S3Key, err)
		}
		if n == 0 {
			return out, nil
		}
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == batchSize {
					done = true
					break
				}
				curLineStart = out.BytesRead
				curLineLength = 0
			}
		}

		if out.EOF {
			done = true
		}
	}

	return
}

func crossrefCrawlWorkflow(ctx workflow.Context, in CrossrefCrawlInput) (counts, error) {
	l := workflow.GetLogger(ctx)
	out := counts{}

	// fetch crossref metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.external_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var skOut skCrossrefOutput
	err := workflow.ExecuteActivity(ctx, skCrossref, in.SKInput).Get(ctx, &skOut)
	if err != nil {
		workflow.GetLogger(ctx).Error("scholkit crossref activity failed:", err)
		return out, err
	}
	workflow.GetLogger(ctx).Info("scholkit crossref s3key:", skOut.S3Key)

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.internal_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key: skOut.S3Key,
	}
	findInput := findLineBatchInput{
		S3Key: skOut.S3Key,
	}
	findOutput := findLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts
	for {
		err := workflow.ExecuteActivity(ctx, findLineBatch, findInput).Get(ctx, &findOutput)
		if err != nil {
			return out, err
		}
		if len(findOutput.Offsets) > 0 {
			findInput.Offset = findOutput.BytesRead
			batchInput.Offsets = findOutput.Offsets
			childWorkflowOptions := workflow.ChildWorkflowOptions{
				ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
			}
			ctx = workflow.WithChildOptions(ctx, childWorkflowOptions)
			fut := workflow.ExecuteChildWorkflow(ctx, lineBatchWorkflow, batchInput)
			var cwe workflow.Execution
			err := fut.GetChildWorkflowExecution().Get(ctx, &cwe)
			if err != nil {
				return out, err
			}
			childSelector.AddFuture(fut, func(f workflow.Future) {
				childErr = f.Get(ctx, &childCounts)
			})
			childCount++
		}
		if findOutput.EOF {
			break
		}
	}

	for range childCount {
		childSelector.Select(ctx)
		if childErr != nil {
			return out, err
		}
		out = out.Add(childCounts)
		l.Info(fmt.Sprintf("child ignored %d lines", childCounts.Releases.Ignored))
	}

	l.Info(fmt.Sprintf("%#v", out))

	return out, nil

	// TODO handle the outcome of a crawl

	// TODO activity: for each fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
	// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
	// TODO activity: bulk ingestion into ES of parsed stuff
}
