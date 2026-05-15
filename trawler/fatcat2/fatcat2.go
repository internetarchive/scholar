package fatcat2

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// maxReleasePayloadBytes is the maximum serialized JSON size we'll POST when
// creating a release.  Coordinated with Django's DATA_UPLOAD_MAX_MEMORY_SIZE
// (currently 10 MB).
const maxReleasePayloadBytes = 10 * 1024 * 1024

type LegacyData struct {
	Ident    uuid.UUID
	Revision uuid.UUID
}

type Container struct {
	ID          uuid.UUID      `json:"id,omitzero"`
	Name        string         `json:"name,omitempty"`
	Type        string         `json:"container_type,omitempty"`
	LegacyRevID *uuid.UUID     `json:"legacy_rev_id"`
	Publisher   string         `json:"publisher,omitempty"`
	ISSNL       string         `json:"issnl,omitempty"`
	ISSNE       string         `json:"issne,omitempty"`
	ISSNP       string         `json:"issnp,omitempty"`
	Source      string         `json:"source,omitempty"`
	Extra       map[string]any `json:"extra"`
}

type Abstract struct {
	ReleaseID *uuid.UUID `json:"release_id"`
	Content   string     `json:"content"`
	SHA1      string     `json:"sha1"`
	Language  string     `json:"language"`
	MIMEType  string     `json:"mimetype"`
}

type ReleaseContrib struct {
	CreatorID      *uuid.UUID     `json:"creator_id"`
	ReleaseID      *uuid.UUID     `json:"release_id"`
	Position       int            `json:"position"`
	RawName        string         `json:"raw_name"`
	GivenName      string         `json:"given_name"`
	Surname        string         `json:"surname"`
	RawAffiliation string         `json:"raw_affiliation"`
	Role           string         `json:"role"`
	Extra          map[string]any `json:"extra"`
}

type ExternalID struct {
	ReleaseID *uuid.UUID `json:"release_id"`
	Type      string     `json:"id_type"`
	Value     string     `json:"id_value"`
}

type ReleaseDate time.Time

func (rd *ReleaseDate) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	s := strings.Trim(string(b), `"`)
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return err
	}
	*rd = ReleaseDate(t)
	return nil
}

func (rd *ReleaseDate) MarshalJSON() ([]byte, error) {
	if rd == nil {
		return json.Marshal(nil)
	}
	t := time.Time(*rd)
	return json.Marshal(t.Format("2006-01-02"))
}

func (rd ReleaseDate) Format(s string) string {
	t := time.Time(rd)
	return t.Format(s)
}

type Release struct {
	ID              uuid.UUID      `json:"id,omitzero"`
	WorkID          *uuid.UUID     `json:"work_id"`
	Title           string         `json:"title,omitempty"`
	OriginalTitle   string         `json:"original_title,omitempty"`
	Subtitle        string         `json:"subtitle,omitempty"`
	Type            string         `json:"release_type,omitempty"`
	Stage           string         `json:"release_stage,omitempty"`
	ReleaseDate     *ReleaseDate   `json:"release_date"`
	ReleaseYear     int            `json:"release_year,omitempty"`
	Source          string         `json:"source,omitempty"`
	Volume          string         `json:"volume,omitempty"`
	Issue           string         `json:"issue,omitempty"`
	Pages           string         `json:"pages,omitempty"`
	Publisher       string         `json:"publisher,omitempty"`
	Language        string         `json:"language,omitempty"`
	LegacyRevID     *uuid.UUID     `json:"legacy_rev_id"`
	LicenseSlug     string         `json:"license_slug,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
	WithdrawnStatus string         `json:"withdrawn_status,omitempty"`
	Number          string         `json:"number,omitempty"`
	Version         string         `json:"version,omitempty"`

	// Foreign keys

	Refs        []RawRef         `json:"refs,omitempty"`
	Abstracts   []Abstract       `json:"abstracts,omitempty"`
	ContainerID *uuid.UUID       `json:"container_id"`
	ExternalIDs []ExternalID     `json:"extids,omitempty"`
	Contribs    []ReleaseContrib `json:"contribs,omitempty"`

	// TODO understand when the structured ReleaseRefs are added in the old system
}

func (r Release) DOI() string {
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doi" {
			return eid.Value
		}
	}
	return ""
}

func (r Release) PMCID() string {
	for _, eid := range r.ExternalIDs {
		if eid.Type == "pmcid" {
			return eid.Value
		}
	}
	return ""
}

func (r Release) ArxivID() string {
	for _, eid := range r.ExternalIDs {
		if eid.Type == "arxiv" {
			return eid.Value
		}
	}
	return ""
}

func (r Release) DoajID() string {
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doaj" {
			return eid.Value
		}
	}
	return ""
}

func (r Release) IsPaperlike() bool {
	paperLikeTypes := []string{
		"article-journal",
		"book",
		"paper-conference",
		"chapter",
		"report",
		"thesis",
	}

	return slices.Contains(paperLikeTypes, r.Type)
}

// FulltextURLs returns a list of possible locations for this release's
// fulltext PDF. These are generated from known URL patterns for the different
// external ID types. Should upstream patterns change, the URLs generated here
// might become useless. The URLs are sorted roughly by preference (IE,
// likelihood of success).
func (r Release) FulltextURLs() []string {
	// TODO this approach smells to me; I'm mostly just preserving what we were
	// doing in fatcat's entity worker. This is important code (drives our daily
	// crawling attempts) _and_ is volatile as it depends on upstream url
	// patterns. I'm keeping it like this for the first pass but having URL
	// templates in config per upstream might be useful to expose.
	/*
	   Relevant fatcat code (python/fatcat_tools/transforms/ingest.py):
	   # generate a URL where we expect to find fulltext
	   url = None
	   link_source = None
	   link_source_id = None
	   if release.ext_ids.arxiv and ingest_type == "pdf":
	       url = "https://arxiv.org/pdf/{}.pdf".format(release.ext_ids.arxiv)
	       link_source = "arxiv"
	       link_source_id = release.ext_ids.arxiv
	   elif release.ext_ids.pmcid and ingest_type == "pdf":
	       # TODO: how to tell if an author manuscript in PMC vs. published?
	       # url = "https://www.ncbi.nlm.nih.gov/pmc/articles/{}/pdf/".format(release.ext_ids.pmcid)
	       url = "http://europepmc.org/backend/ptpmcrender.fcgi?accid={}&blobtype=pdf".format(
	           release.ext_ids.pmcid
	       )
	       link_source = "pmc"
	       link_source_id = release.ext_ids.pmcid
	   elif release.ext_ids.doi:
	       url = "https://doi.org/{}".format(release.ext_ids.doi.lower())
	       link_source = "doi"
	       link_source_id = release.ext_ids.doi.lower()
	   elif release.ext_ids.doaj:
	       url = "https://doaj.org/article/{}".format(release.ext_ids.doaj.lower())
	       link_source = "doaj"
	       link_source_id = release.ext_ids.doaj.lower()
	   elif release.ext_ids.hdl:
	       url = "https://hdl.handle.net/{}".format(release.ext_ids.hdl.lower())
	       link_source = "hdl"
	       link_source_id = release.ext_ids.hdl.lower()
	*/

	out := []string{}

	if r.ArxivID() != "" {
		out = append(out, fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", r.ArxivID()))
	}

	if r.PMCID() != "" {
		out = append(out, fmt.Sprintf(
			"http://europepmc.org/backend/ptpmcrender.fcgi?accid=%s&blobtype=pdf", r.PMCID()))
	}

	if r.DoajID() != "" {
		if doajExtra, ok := r.Extra["doaj"]; ok {
			if ftu, ok := doajExtra.(map[string]any)["full_text_url"]; ok {
				out = append(out, ftu.(string))
			}
		}
		out = append(out, fmt.Sprintf("https://doaj.org/article/%s", r.DoajID()))
	}

	if r.DOI() != "" {
		out = append(out, fmt.Sprintf("https://doi.org/%s", r.DOI()))
	}

	// TODO hdl
	return out
}

// RawRef is stored in fatcat2's database as a json value in a release row
type RawRef struct {
	// TODO I don't like how this is structured (wayyy too much shoved in extra)
	// but just maintaining parity for now with legacy fatcat

	// NB no indication TargetReleaseID is ever set
	// TODO this is ending up as uuid.Nil
	//TargetReleaseID *uuid.UUID     `json:"target_release_id,omitempty"`
	Title         string         `json:"title,omitempty"`
	Index         int            `json:"index,omitempty"`
	Key           string         `json:"key,omitempty"`
	Year          int            `json:"year,omitempty"`
	ContainerName string         `json:"container_name,omitempty"`
	Locator       string         `json:"locator,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

type FileURL struct {
	FileID uuid.UUID `json:"file_id,omitzero"`
	Rel    string    `json:"rel"`
	URL    string    `json:"url"`
	Source string    `json:"source"`
}

type File struct {
	Releases    []Release  `json:"releases"`
	URLs        []FileURL  `json:"urls"`
	ID          uuid.UUID  `json:"id,omitzero"`
	Source      string     `json:"source"`
	Size        int        `json:"size_bytes"`
	Sha1        string     `json:"sha1"`
	Sha256      string     `json:"sha256"`
	Md5         string     `json:"md5"`
	Mimetype    string     `json:"mimetype"`
	LegacyRevID *uuid.UUID `json:"legacy_rev_id"`
}

// SetMetadata takes a byte array and sets the various checksum fields and the
// byte size field on this File struct
func (f *File) SetMetadata(bs []byte) error {

	md5h := md5.New()
	if _, err := io.Copy(md5h, bytes.NewBuffer(bs)); err != nil {
		return fmt.Errorf("could not md5 sum pdf bytes: %w", err)
	}

	sha1h := sha1.New()
	if _, err := io.Copy(sha1h, bytes.NewBuffer(bs)); err != nil {
		return fmt.Errorf("could not sha1 sum pdf bytes: %w", err)
	}

	sha256h := sha256.New()
	if _, err := io.Copy(sha256h, bytes.NewBuffer(bs)); err != nil {
		return fmt.Errorf("could not sha256 sum pdf bytes: %w", err)
	}

	f.Sha1 = fmt.Sprintf("%x", sha1h.Sum(nil))
	f.Sha256 = fmt.Sprintf("%x", sha256h.Sum(nil))
	f.Md5 = fmt.Sprintf("%x", md5h.Sum(nil))
	f.Size = len(bs)

	return nil
}

type Creator struct {
	ID          uuid.UUID `json:"id,omitzero"`
	DisplayName string    `json:"display_name,omitempty"`
	GivenName   string    `json:"given_name,omitempty"`
	Surname     string    `json:"surname,omitempty"`
	Orcid       string    `json:"orcid,omitempty"`

	// TODO Entity fields like source, timestamps, etc
}

// CreateContainer creates a new container in fc2 and returns its ID
func CreateContainer(client *http.Client, c *Container) (*uuid.UUID, error) {
	var err error
	c.ID, err = uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid creation failed: %w", err)
	}

	legacy, err := lookupLegacyContainer(client, c.ISSNL)
	if err != nil {
		return nil, fmt.Errorf("legacy lookup failed: %w", err)
	}

	if legacy != nil {
		c.ID = legacy.Ident
		c.LegacyRevID = &legacy.Revision
	}

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
		return nil, fmt.Errorf("container POST failed for '%#v': %w", c, err)
	}

	if resp.StatusCode == 422 {
		// container already exists, likely a retry; treat as success
		return &c.ID, nil
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code for '%#v' POST: %d; body '%s'", c, resp.StatusCode, b)
	}

	return &c.ID, nil
}

// CreateFile creates a new file in fc2 and returns its ID
func CreateFile(client *http.Client, f *File) (*uuid.UUID, error) {
	if f.ID == uuid.Nil {
		var err error
		f.ID, err = uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("uuid creation failed: %w", err)
		}
	}

	legacy, err := lookupLegacyFile(client, f.Sha1)
	if err != nil {
		return nil, fmt.Errorf("legacy lookup failed: %w", err)
	}

	if legacy != nil {
		f.ID = legacy.Ident
		f.LegacyRevID = &legacy.Revision
	}

	fc2url := viper.GetString("fatcat2.endpoint")
	fc2key := viper.GetString("fatcat2.key")

	bs, err := json.Marshal(f)

	body := bytes.NewBuffer(bs)
	req, err := http.NewRequest("POST", fc2url+"/file", body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", fc2key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("file POST failed for '%#v': %w", f, err)
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code for '%#v' POST: %d; body '%s'", f, resp.StatusCode, b)
	}

	return &f.ID, nil
}

// CreateRelease creates a new release in fc2 and returns its ID
func CreateRelease(client *http.Client, r Release) (*uuid.UUID, error) {
	var err error
	r.ID, err = uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid creation failed: %w", err)
	}

	if len(r.ExternalIDs) == 0 {
		panic("nothing without an external ID should get to this point")
	}

	var legacy *LegacyData
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doaj" {
			continue
		}
		legacy, err = lookupLegacyRelease(client, eid.Type, eid.Value)
		if err != nil {
			return nil, fmt.Errorf("legacy lookup failed: %w", err)
		}
		if legacy != nil {
			r.ID = legacy.Ident
			r.LegacyRevID = &legacy.Revision
			break
		}
	}

	fc2url := viper.GetString("fatcat2.endpoint")
	fc2key := viper.GetString("fatcat2.key")

	bs, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("release marshal failed: %w", err)
	}

	if len(bs) > maxReleasePayloadBytes {
		return nil, fmt.Errorf("release payload too large: %d bytes (max %d)", len(bs), maxReleasePayloadBytes)
	}

	body := bytes.NewBuffer(bs)
	req, err := http.NewRequest("POST", fc2url+"/release", body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", fc2key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("release POST failed for '%#v': %w", r, err)
	}

	if resp.StatusCode == 409 {
		// release already exists, likely a retry; treat as success
		return &r.ID, nil
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code for %q (%s) POST: %d; body '%s'",
			r.ID, r.LegacyRevID, resp.StatusCode, b)
	}

	return &r.ID, nil
}

func ReleaseFiles(c *http.Client, rid uuid.UUID) ([]File, error) {
	type payload struct {
		Items []File
	}
	out := []File{}
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/release/"+rid.String()+"/files", nil)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 /release/%s/files failed: %w", rid.String(), err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("could not read release '%s' files: %w", rid.String(), err)
	}

	if resp.StatusCode != 200 {
		return out, fmt.Errorf("fc2 /release/%s/files returned %d: %s", rid.String(), resp.StatusCode, bs)
	}

	var p payload
	err = json.Unmarshal(bs, &p)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal release '%s' files: %w", rid.String(), err)
	}

	return p.Items, nil
}

// TODO generalize
func GetRelease(c *http.Client, id uuid.UUID) (Release, error) {
	out := Release{}
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/release/"+id.String(), nil)
	if err != nil {
		panic(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 /release/%s failed: %w", id.String(), err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("could not read release '%s': %w", id.String(), err)
	}

	if resp.StatusCode != 200 {
		return out, fmt.Errorf("fc2 /release/%s returned %d: %s", id.String(), resp.StatusCode, bs)
	}

	err = json.Unmarshal(bs, &out)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal release '%s': %w", id.String(), err)
	}

	return out, nil
}

func GetFile(c *http.Client, id uuid.UUID) (File, error) {
	out := File{}
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/file/"+id.String(), nil)
	if err != nil {
		panic(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 /file/%s failed: %w", id.String(), err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("could not read file '%s': %w", id.String(), err)
	}

	if resp.StatusCode != 200 {
		return out, fmt.Errorf("fc2 /file/%s returned %d: %s", id.String(), resp.StatusCode, bs)
	}

	err = json.Unmarshal(bs, &out)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal file '%s': %w", id.String(), err)
	}

	return out, nil
}

// GetContainer looks up a container via its ID
func GetContainer(c *http.Client, id uuid.UUID) (Container, error) {
	out := Container{}
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/container/"+id.String(), nil)
	if err != nil {
		panic(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 /container/%s failed: %w", id.String(), err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("could not read container '%s': %w", id.String(), err)
	}

	if resp.StatusCode != 200 {
		return out, fmt.Errorf("fc2 /container/%s returned %d: %s", id.String(), resp.StatusCode, bs)
	}

	err = json.Unmarshal(bs, &out)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal container '%s': %w", id.String(), err)
	}

	return out, nil
}

// GetCreator looks up a creator via its ID
func GetCreator(c *http.Client, id uuid.UUID) (Creator, error) {
	out := Creator{}
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/creator/"+id.String(), nil)
	if err != nil {
		panic(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return out, fmt.Errorf("fc2 /creator/%s failed: %w", id.String(), err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("could not read creator '%s': %w", id.String(), err)
	}

	err = json.Unmarshal(bs, &out)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal creator '%s': %w", id.String(), err)
	}

	return out, nil
}

func lookupLegacy(c *http.Client, endpoint, idtype, idvalue string) (*LegacyData, error) {
	if idtype == "doi" {
		if !cleaning.IsAscii(idvalue) {
			// fatcat v1 does not support unicode in DOI even though UTF-8 is allowed.
			return nil, nil
		}

	}
	fc1url := viper.GetString("fatcat1.endpoint")
	req, err := http.NewRequest("GET", fc1url+"/"+endpoint, nil)
	if err != nil {
		panic(err)
	}
	q := req.URL.Query()
	q.Add("extid_type", idtype)
	q.Add("extid_value", idvalue)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fc1 lookup failed for %s of '%s': %w", idtype, idvalue, err)
	}
	if resp.StatusCode == 404 {
		return nil, nil
	}

	if resp.StatusCode != 200 {
		// NB invalid issnls crash the server (500). if we get bad data from
		// crossref it will stop the activity cold. might have to patch fc1 to
		// return a 400.
		return nil, fmt.Errorf(
			"did not get 200 nor 404 from fc1 for %s of '%s' lookup: %d",
			idtype, idvalue, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var p struct {
		Ident    string
		Revision uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return nil, fmt.Errorf("unmarshal failed for %s of '%s': %w", idtype, idvalue, err)
	}

	ident, err := fc2uuid(p.Ident)
	if err != nil {
		return nil, err
	}

	return &LegacyData{
		Ident:    ident,
		Revision: p.Revision,
	}, nil
}

func lookupLegacyContainer(c *http.Client, issnl string) (*LegacyData, error) {
	return lookupLegacy(c, "lookup_container", "issnl", issnl)
}

func lookupLegacyRelease(c *http.Client, idtype, idvalue string) (*LegacyData, error) {
	return lookupLegacy(c, "lookup_release", idtype, idvalue)
}

func lookupLegacyFile(c *http.Client, sha1 string) (*LegacyData, error) {
	return lookupLegacy(c, "lookup_file", "sha1", sha1)
}

func lookup(c *http.Client, entityType, idType, idValue string) (*uuid.UUID, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/"+entityType+"/lookup", nil)
	if err != nil {
		panic(err)
	}

	q := req.URL.Query()
	q.Add("id_type", idType)
	q.Add("id_value", idValue)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fc2 lookup failed for '%s': %w", idValue, err)
	}

	if resp.StatusCode == 404 {
		return nil, nil
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			idValue, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var p struct {
		ID uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return nil, fmt.Errorf("unmarshal failed for '%s': %w", idValue, err)
	}

	return &p.ID, nil
}

// LookupDoi returns the ID of a fatcat2 Release with the given DOI, if any.
func LookupDoi(c *http.Client, doi string) (*uuid.UUID, error) {
	return lookup(c, "release", "doi", doi)
}

// LookupPmid returns the ID of a fatcat2 Release with the given PMID, if any.
func LookupPmid(c *http.Client, pmid string) (*uuid.UUID, error) {
	return lookup(c, "release", "pmid", pmid)
}

// LookupArxiv returns the ID of a fatcat2 Release with the given arXiv ID, if any.
func LookupArxiv(c *http.Client, arxivID string) (*uuid.UUID, error) {
	return lookup(c, "release", "arxiv", arxivID)
}

// LookupOrcid returns the ID of a fatcat2 Creator with the given orcid, if any.
func LookupOrcid(c *http.Client, orcid string) (*uuid.UUID, error) {
	return lookup(c, "creator", "orcid", orcid)
}

// LookupIssnl returns the ID of a fatcat2 Container with the given ISSNL, if any.
func LookupIssnl(c *http.Client, issnl string) (*uuid.UUID, error) {
	return lookup(c, "container", "issnl", issnl)
}

// LookupDoaj returns the ID of a fatcat2 Release with the given DOAJ article ID, if any.
func LookupDoaj(c *http.Client, doajID string) (*uuid.UUID, error) {
	return lookup(c, "release", "doaj", doajID)
}

// LookupSha256 returns the ID of a fatcat2 File with the given Sha256, if any.
func LookupSha256(c *http.Client, sha256 string) (*uuid.UUID, error) {
	return lookup(c, "file", "sha256", sha256)
}

// LookupSha1 returns the ID of a fatcat2 File with the given Sha1, if any.
func LookupSha1(c *http.Client, sha1 string) (*uuid.UUID, error) {
	return lookup(c, "file", "sha1", sha1)
}

func fc2uuid(fatcatIdent string) (uuid.UUID, error) {
	i := strings.ToUpper(fatcatIdent + "======")
	decoded, err := base32.StdEncoding.DecodeString(i)
	if err != nil {
		return uuid.Nil, err
	}

	u, err := uuid.FromBytes(decoded)
	if err != nil {
		return uuid.Nil, err
	}

	if u == uuid.Nil {
		return u, errors.New("got empty uuid")
	}

	return u, nil
}

func UuidToLegacy(u uuid.UUID) string {
	return strings.ToLower(base32.StdEncoding.EncodeToString(u[:])[:26])
}
