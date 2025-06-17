// package spn implements a thin wrapper around the Wayback team's
// SavePageNow API. It does not implement 100% of what the API can do but
// covers most of it.
package spn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// type SystemStatus describes the result of the /status/system endpoint
type SystemStatus struct {
	Status string
}

// type UserStatus describes the result of the /status/user endpoint
type UserStatus struct {
	Available  int
	Processing int
	// TODO i swear there is more (slots?)
}

// type JobStatus describes the result of the /status/<job id> endpoint
type JobStatus struct {
	Status      string
	JobID       string
	OriginalURL string `json:"original_url"`
	Screenshot  string
	Timestamp   string
	Duration    float64
	Resources   []string
	Outlinks    map[string]string
}

// type SaveResult describes the result of requesting a page save via "POST /save"
type SaveResult struct {
	URL   string
	JobID string
}

// type SPNConfig describes the configuration needed for the SPN API to
// function.
type SPNConfig struct {
	accessKey string
	secretKey string
	endpoint  string
}

// type SaveRequest describes the arguments both required and optional for a
// call to "POST /save". It largely tracks the naming conventions of the API
// with a few exceptions.
type SaveRequest struct {
	// URL (url) to capture.
	URL string

	// CaptureAll (capture_all) dictates whether or not to capture non-200
	// statuses.
	CaptureAll bool

	// CaptureOutlinks (capture_outlinks) dictates whether or not to enqueue
	// additional jobs in
	// order to capture the outlinks on the initial page.
	CaptureOutlinks bool

	// CaptureScreenshot (capture_screenshot) dictates whether or not to ask SPN
	// to save a screenshot of a page it is saving.
	CaptureScreenshot bool

	// DelayWBAvailability (delay_wb_availability) tells SPN to not list a saved
	// page in the WaybackMachine for up to 12 hours (in order to reduce SPN
	// load).
	DelayWBAvailability bool

	// ForceGet (force_get) instructs SPN to use a simple HTTP GET to capture a
	// page instead of a headless browser.
	ForceGet bool

	// SkipFirstArchive (skip_first_archive) instructs SPN to skip checking
	// whether or not a given capture is the first for its URL. Captures will run
	// faster if this is disabled.
	SkipFirstArchive bool

	// IfNotArchivedWithinSecs (if_not_archived_within) instructs SPN to only
	// capture a given URL if it hasn't been captured within the given timeframe.
	// The SPN API accepts arbitrary time duration strings; this client only
	// supports an integer seconds value. Also unsupported is the ability to pass
	// multiple timedeltas (one for base URL and one for outlinks).
	IfNotArchivedWithinSecs int

	// OutlinksAvailability (outlinks_availability) instructs SPN to return the
	// timestamp of last capture for all outlinks.
	OutlinksAvailability bool

	// EmailResult (email_result) instructs SPN to send results to the email
	// address associated with the access/secret key.
	EmailResult bool

	// DelayForJavascript (js_behavior_timeout=0), if false, instructs SPN to not
	// wait for Javascript to run during captures.
	DelayForJavascript bool

	// JavascriptTimeout (js_behavior_timeout=N, N>0) specifies how long in
	// seconds SPN should wait for Javascript to run during captures where
	// DelayForJavascript is true.
	JavascriptTimeout int

	// CaptureCookie (capture_cookie), if set, will be used by the SPN crawler
	// during requests.
	CaptureCookie string

	// UserAgent (use_user_agent) specifies what user agent string SPN should use
	// when making requests.
	UserAgent string

	// TargetUsername (target_username) specifies a username to use when SPN
	// encounters login forms.
	TargetUsername string

	// TargetPassword (target_password) specifies a password to use when SPN
	// encounters login forms.
	TargetPassword string
}

// type Client describes an SPN client.
type Client interface {
	StatusSystem() (SystemStatus, error)
	StatusUser() (UserStatus, error)
	StatusJob(string) (JobStatus, error)
	Save(SaveRequest) SaveResult
}

// type DefaultClient is the basic, concrete implementation for an SPN client.
type DefaultClient struct {
	Config SPNConfig
	client *http.Client
}

func (c *DefaultClient) newRequest(method string, path string, body io.Reader) (*http.Request, error) {
	p, err := url.JoinPath(c.Config.endpoint, path)
	if err != nil {
		return nil, fmt.Errorf("could not join: %w", err)
	}

	req, err := http.NewRequest(method, p, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create req: %w", err)
	}

	req.Header.Add("User-Agent", "Mozilla/5.0 scholar-go-spn-client")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization",
		fmt.Sprintf("LOW %s:%s", c.Config.accessKey, c.Config.secretKey))

	return req, nil
}

func (c *DefaultClient) do(method, path string, body io.Reader, parsed any) error {
	req, err := c.newRequest(method, path, body)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("SPN call failed: %w", err)
	}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read SPN response: %w", err)
	}

	err = json.Unmarshal(rbody, parsed)
	if err != nil {
		return fmt.Errorf("could not parse SPN response: %w", err)
	}

	return nil
}

func (c *DefaultClient) StatusSystem() (SystemStatus, error) {
	out := SystemStatus{}

	err := c.do("GET", "status/system", nil, &out)
	if err != nil {
		return out, fmt.Errorf("status/system failed: %w", err)
	}

	return out, nil
}

func (c *DefaultClient) StatusUser() (UserStatus, error) {
	out := UserStatus{}

	err := c.do("GET", "status/user", nil, &out)
	if err != nil {
		return out, fmt.Errorf("status/user failed: %w", err)
	}

	return out, nil
}

func (c *DefaultClient) StatusJob(jobID string) (JobStatus, error) {
	out := JobStatus{}

	p := fmt.Sprintf("status/%s", jobID)

	err := c.do("GET", p, nil, &out)
	if err != nil {
		return out, fmt.Errorf("%s failed: %w", p, err)
	}

	return out, nil
}

func (c *DefaultClient) Save(req SaveRequest) SaveResult {
	// TODO
	return SaveResult{}
}

type TestClient struct {
	// TODO
}

// NewDefaultClient creates an SPN DefaultClient. It accepts configuration that
// must include an access key and an access secret; endpoint is optional.
func NewDefaultClient(cfg SPNConfig) (Client, error) {
	if cfg.endpoint == "" {
		cfg.endpoint = "web.archive.org/save"
	}
	if cfg.accessKey == "" {
		return nil, errors.New("cannot create spn client without access key")
	}
	if cfg.secretKey == "" {
		return nil, errors.New("cannot create spn client without secret key")
	}
	return &DefaultClient{
		Config: cfg,
		client: &http.Client{},
	}, nil
}
