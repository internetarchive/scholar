package kbart

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/internetarchive/scholar/kbart/internal/fatcat"
)

// BasicThreshold is the minimum count used throughout the eligibility checks
// (BASIC_THRESHOLD in fatcat_kbart.py).
const BasicThreshold = 15

// paperReleaseTypes are the release types counted as "papers" (PAPER_RELEASE_TYPES).
var paperReleaseTypes = map[string]bool{
	"article-journal":  true,
	"conference_paper": true,
	"article":          true,
	"post":             true,
	"report":           true,
	"retraction":       true,
}

// containerTypeBlocklist are container types that are never eligible.
var containerTypeBlocklist = map[string]bool{
	"book-series": true,
	"blog":        true,
	"magazine":    true,
	"trade":       true,
	"test":        true,
	"repository":  true,
	"archive":     true,
}

// StatusSuccess is the status string for an eligible container.
const StatusSuccess = "success"

// Info is the full set of fatcat data for one container. It is the shape
// dumped by `report --dump-json` and consumed by `report --from-json`.
type Info struct {
	Ident     string                `json:"ident"`
	UUID      string                `json:"uuid"`
	Container *fatcat.Container     `json:"container"`
	Stats     *fatcat.Stats         `json:"stats"`
	ByYear    []fatcat.YearBucket   `json:"by_year"`
	ByVolume  []fatcat.VolumeBucket `json:"by_volume"`
	ByType    []fatcat.TypeBucket   `json:"by_type"`
	Status    string                `json:"status"`
	Eligible  bool                  `json:"is_eligible"`

	// Row is the built KBART row for an eligible container. It is not part of
	// the JSON dump; --from-json rebuilds it.
	Row *Row `json:"-"`
}

// Source lazily provides the fatcat data needed for eligibility. Implementations
// memoize, so evaluate() only triggers the fetches the check order requires.
type Source interface {
	Container() (*fatcat.Container, error)
	Stats() (*fatcat.Stats, error)
	ByYear() ([]fatcat.YearBucket, error)
	ByVolume() ([]fatcat.VolumeBucket, error)
	ByType() ([]fatcat.TypeBucket, error)
}

// memo caches a single lazily-computed (value, error).
type memo[T any] struct {
	done bool
	val  T
	err  error
}

func (m *memo[T]) get(fn func() (T, error)) (T, error) {
	if !m.done {
		m.val, m.err = fn()
		m.done = true
	}
	return m.val, m.err
}

// LiveSource fetches from the fatcat v2 API on demand, memoizing each endpoint.
// A LiveSource is used by a single goroutine, so it needs no locking.
type LiveSource struct {
	FC    *fatcat.Client
	UUID  string
	Ident string

	container memo[*fatcat.Container]
	stats     memo[*fatcat.Stats]
	byYear    memo[[]fatcat.YearBucket]
	byVolume  memo[[]fatcat.VolumeBucket]
	byType    memo[[]fatcat.TypeBucket]
}

func (s *LiveSource) Container() (*fatcat.Container, error) {
	return s.container.get(func() (*fatcat.Container, error) {
		c, err := s.FC.Container(s.UUID)
		if err == nil && c != nil {
			c.Ident = s.Ident // base32 ident is not in the API response
		}
		return c, err
	})
}
func (s *LiveSource) Stats() (*fatcat.Stats, error) {
	return s.stats.get(func() (*fatcat.Stats, error) { return s.FC.Stats(s.UUID) })
}
func (s *LiveSource) ByYear() ([]fatcat.YearBucket, error) {
	return s.byYear.get(func() ([]fatcat.YearBucket, error) { return s.FC.PreservationByYear(s.UUID) })
}
func (s *LiveSource) ByVolume() ([]fatcat.VolumeBucket, error) {
	return s.byVolume.get(func() ([]fatcat.VolumeBucket, error) { return s.FC.PreservationByVolume(s.UUID) })
}
func (s *LiveSource) ByType() ([]fatcat.TypeBucket, error) {
	return s.byType.get(func() ([]fatcat.TypeBucket, error) { return s.FC.PreservationByType(s.UUID) })
}

// staticSource serves preloaded data (used by --from-json and --dump-json).
type staticSource struct{ info *Info }

func (s staticSource) Container() (*fatcat.Container, error) { return s.info.Container, nil }
func (s staticSource) Stats() (*fatcat.Stats, error)         { return s.info.Stats, nil }
func (s staticSource) ByYear() ([]fatcat.YearBucket, error)  { return s.info.ByYear, nil }
func (s staticSource) ByVolume() ([]fatcat.VolumeBucket, error) {
	return s.info.ByVolume, nil
}
func (s staticSource) ByType() ([]fatcat.TypeBucket, error) { return s.info.ByType, nil }

// NewStaticSource wraps an already-populated Info as a Source. It also restores
// Container.Ident (dropped by JSON marshalling) from Info.Ident.
func NewStaticSource(info *Info) Source {
	if info.Container != nil && info.Container.Ident == "" {
		info.Container.Ident = info.Ident
	}
	return staticSource{info: info}
}

// yearFullyBright reports whether a year bucket has no dark/none releases.
// (shadows_only, referenced by the original filter, no longer exists in v2 and
// is treated as zero.)
func yearFullyBright(b fatcat.YearBucket) bool  { return b.Dark == 0 && b.None == 0 }
func volFullyBright(b fatcat.VolumeBucket) bool { return b.Dark == 0 && b.None == 0 }

func filterYears(vals []fatcat.YearBucket) []fatcat.YearBucket {
	out := make([]fatcat.YearBucket, 0, len(vals))
	for _, v := range vals {
		if yearFullyBright(v) {
			out = append(out, v)
		}
	}
	return out
}

func filterVolumes(vals []fatcat.VolumeBucket) []fatcat.VolumeBucket {
	out := make([]fatcat.VolumeBucket, 0, len(vals))
	for _, v := range vals {
		if volFullyBright(v) {
			out = append(out, v)
		}
	}
	return out
}

// Evaluate runs the eligibility checks in the exact order of the original
// eligible_status(), returning a status string ("success" when eligible). It
// fetches from src lazily, so failing an early check avoids the later fetches.
// A fetch error is returned separately (the caller logs and skips, like the
// Python RetryError path).
func Evaluate(src Source) (string, error) {
	container, err := src.Container()
	if err != nil {
		return "", err
	}
	if container == nil {
		return "", fmt.Errorf("nil container")
	}

	if containerTypeBlocklist[container.ContainerType] {
		return "container-type", nil
	}
	if container.ISSNL == "" {
		return "missing-issnl", nil
	}

	// Copy deprecated ISSN-E/ISSN-P out of extra to the canonical fields. In v2
	// these are usually already top-level; the fallback matches the Python and
	// affects the KBART output too.
	if container.ISSNE == "" {
		container.ISSNE = extraString(container.Extra, "issne")
	}
	if container.ISSNP == "" {
		container.ISSNP = extraString(container.Extra, "issnp")
	}
	if container.ISSNE == "" && container.ISSNP == "" {
		return "missing-issn", nil
	}

	stats, err := src.Stats()
	if err != nil {
		return "", err
	}
	if stats.Total < BasicThreshold {
		return "few-releases", nil
	}
	if stats.Preservation.Total == 0 ||
		float64(stats.Preservation.Bright)/float64(stats.Preservation.Total) < 0.8 {
		return "low-overall-preservation-fraction", nil
	}

	// Convert from a "release" basis to a "papers" basis. papersTotal comes from
	// stats alone; papersPreserved needs the by_type histogram, so that fetch is
	// deferred until the two papersTotal-only checks pass.
	papersTotal := 0
	for k, v := range stats.ReleaseType {
		if paperReleaseTypes[k] {
			papersTotal += v
		}
	}
	if papersTotal < BasicThreshold {
		return "few-papers", nil
	}
	if float64(papersTotal)/float64(stats.Total) < 0.8 {
		return "low-paper-fraction", nil
	}

	byType, err := src.ByType()
	if err != nil {
		return "", err
	}
	papersPreserved := 0
	for _, b := range byType {
		if paperReleaseTypes[b.ReleaseType] {
			papersPreserved += b.Bright
		}
	}
	if papersPreserved < BasicThreshold {
		return "few-preserved-papers", nil
	}
	if float64(papersPreserved)/float64(papersTotal) < 0.8 {
		return "low-paper-preservation-fraction", nil
	}

	rawByYear, err := src.ByYear()
	if err != nil {
		return "", err
	}
	rawByVolume, err := src.ByVolume()
	if err != nil {
		return "", err
	}
	byYear := filterYears(rawByYear)
	byVolume := filterVolumes(rawByVolume)

	if len(rawByYear) == 0 {
		return "no-year-spans", nil
	}
	if len(rawByVolume) == 0 {
		return "no-volume-spans", nil
	}
	if len(byYear) == 0 {
		return "no-preserved-year-spans", nil
	}
	if len(byVolume) == 0 {
		return "no-preserved-volume-spans", nil
	}
	if len(byYear) < 2 {
		return "short-preserved-year-spans", nil
	}
	if len(byVolume) < 2 {
		return "short-preserved-volume-spans", nil
	}

	// Count full raw_by_* because we skip whole years if a single release is
	// missing.
	preservedWithYear := 0
	for _, b := range rawByYear {
		preservedWithYear += b.Bright
	}
	preservedWithVolume := 0
	for _, b := range rawByVolume {
		preservedWithVolume += b.Bright
	}
	if preservedWithYear < BasicThreshold {
		return "few-preserved-with-year", nil
	}
	if preservedWithVolume < BasicThreshold {
		return "few-preserved-with-volume", nil
	}
	if float64(preservedWithYear)/float64(papersTotal) < 0.8 {
		return "low-preserved-with-year-fraction", nil
	}
	if float64(preservedWithVolume)/float64(papersTotal) < 0.8 {
		return "low-preserved-with-volume-fraction", nil
	}

	// Contiguity of the fully-preserved year and volume spans. Volumes are
	// strings in v2; a non-integer volume can't satisfy contiguity, so it is a
	// rejection rather than the crash the Python would have hit.
	years := sortedUniqueYears(byYear)
	if years[len(years)-1]-years[0]+1 != len(years) {
		return "non-contiguous-years", nil
	}
	volumes, ok := sortedUniqueVolumes(byVolume)
	if !ok {
		return "non-integer-volumes", nil
	}
	if volumes[len(volumes)-1]-volumes[0]+1 != len(volumes) {
		return "non-contiguous-volumes", nil
	}

	return StatusSuccess, nil
}

// ToRow builds a KBART Row for an eligible container. thisYear is the current
// year: a span whose last year equals it has its last year/volume decremented
// by one (the current year is assumed incomplete), matching the Python.
func ToRow(src Source, thisYear int) (Row, error) {
	container, err := src.Container()
	if err != nil {
		return Row{}, err
	}
	rawByYear, err := src.ByYear()
	if err != nil {
		return Row{}, err
	}
	rawByVolume, err := src.ByVolume()
	if err != nil {
		return Row{}, err
	}
	byYear := filterYears(rawByYear)
	byVolume := filterVolumes(rawByVolume)
	if len(byYear) == 0 || len(byVolume) == 0 {
		return Row{}, fmt.Errorf("ToRow: empty preserved spans for %s", container.Ident)
	}

	firstYear := byYear[0].Year
	lastYear := byYear[len(byYear)-1].Year
	// Volumes are strings in v2 but were integers in the old API; normalize to
	// integer form so output matches the historical files (e.g. "01" -> "1").
	firstVol, err := strconv.Atoi(byVolume[0].Volume)
	if err != nil {
		return Row{}, fmt.Errorf("ToRow: non-integer first volume %q for %s", byVolume[0].Volume, container.Ident)
	}
	lastVol, err := strconv.Atoi(byVolume[len(byVolume)-1].Volume)
	if err != nil {
		return Row{}, fmt.Errorf("ToRow: non-integer last volume %q for %s", byVolume[len(byVolume)-1].Volume, container.Ident)
	}
	if lastYear == thisYear {
		lastYear--
		lastVol--
	}

	return Row{
		PublicationType:      "serial",
		PublicationTitle:     container.Name,
		PrintIdentifier:      container.ISSNP,
		OnlineIdentifier:     container.ISSNE,
		DateFirstIssueOnline: strconv.Itoa(firstYear),
		NumFirstVolOnline:    strconv.Itoa(firstVol),
		DateLastIssueOnline:  strconv.Itoa(lastYear),
		NumLastVolOnline:     strconv.Itoa(lastVol),
		TitleID:              "container_" + container.Ident,
		CoverageDepth:        "fulltext",
		PublisherName:        container.Publisher,
		LinkingISSN:          container.ISSNL,
	}, nil
}

func sortedUniqueYears(b []fatcat.YearBucket) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range b {
		if !seen[v.Year] {
			seen[v.Year] = true
			out = append(out, v.Year)
		}
	}
	sort.Ints(out)
	return out
}

func sortedUniqueVolumes(b []fatcat.VolumeBucket) ([]int, bool) {
	seen := map[int]bool{}
	var out []int
	for _, v := range b {
		n, err := strconv.Atoi(v.Volume)
		if err != nil {
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, true
}

func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	if s, ok := extra[key].(string); ok {
		return s
	}
	return ""
}
