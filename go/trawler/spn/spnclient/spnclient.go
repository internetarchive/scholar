// package spnclient implements a thin wrapper around the Wayback team's
// SavePageNow API. It does not implement 100% of what the API can do but
// covers most of it.
package spnclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// type SystemStatus describes the result of the /status/system endpoint
type SystemStatus struct {
	Status string `json:"status"`
}

// type UserStatus describes the result of the /status/user endpoint
type UserStatus struct {
	Available  int `json:"available"`
	Processing int `json:"processing"`
}

// type JobStatus describes the result of the /status/<job id> endpoint
type JobStatus struct {
	Status      string            `json:"status"`
	JobID       string            `json:"job_id"`
	OriginalURL string            `json:"original_url"`
	Screenshot  string            `json:"screenshot"`
	Timestamp   string            `json:"timestamp"`
	Duration    float64           `json:"duration"`
	Resources   []string          `json:"resources"`
	Outlinks    map[string]string `json:"outlinks"`
}

// type SaveResult describes the result of requesting a page save via "POST /save"
type SaveResult struct {
	URL     string `json:"url"`
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

// type SPNConfig describes the configuration needed for the SPN API to
// function.
type SPNConfig struct {
	AccessKey string
	SecretKey string
	Endpoint  string
	Debug     bool
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
	Save(SaveRequest) (SaveResult, error)
}

// type DefaultClient is the basic, concrete implementation for an SPN client.
type DefaultClient struct {
	Config SPNConfig
	client *http.Client
	Debug  bool
}

func (c *DefaultClient) newRequest(method string, path string, body io.Reader) (*http.Request, error) {
	p, err := url.JoinPath("https://"+c.Config.Endpoint, path)
	if err != nil {
		return nil, fmt.Errorf("could not join: %w", err)
	}

	req, err := http.NewRequest(method, p, body)
	if err != nil {
		return nil, fmt.Errorf("could not create req: %w", err)
	}

	req.Header.Add("User-Agent", "Mozilla/5.0 scholar-go-spn-client")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("LOW %s:%s", c.Config.AccessKey, c.Config.SecretKey))

	if method == "POST" {
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}

	return req, nil
}

func (c *DefaultClient) do(method, path string, body io.Reader, parsed any) error {
	req, err := c.newRequest(method, path, body)
	if err != nil {
		return err
	}

	if c.Debug {
		fmt.Printf("-> %s %s\n", method, req.URL)
		for k, v := range req.Header {
			if k == "Authorization" {
				v = []string{"***:***"}
			}
			fmt.Printf("-> %s: %s\n", k, v)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("SPN call failed: %w", err)
	}

	if c.Debug {
		fmt.Printf("<- %d\n", resp.StatusCode)
	}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read SPN response: %w", err)
	}

	if c.Debug {
		fmt.Println(string(rbody))
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

func toIntBoolArg(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// Save POSTSs a URL to the /save endpoint. See the documentation
// for SaveRequest to see how to form the call's arguments.
func (c *DefaultClient) Save(req SaveRequest) (SaveResult, error) {
	out := SaveResult{}

	if req.URL == "" {
		return out, errors.New("URL is required")
	}

	values := url.Values{}
	values.Add("url", req.URL)
	values.Add("capture_all", toIntBoolArg(req.CaptureAll))
	values.Add("capture_outlinks", toIntBoolArg(req.CaptureOutlinks))
	values.Add("capture_screenshot", toIntBoolArg(req.CaptureScreenshot))
	values.Add("delay_wb_availability", toIntBoolArg(req.DelayWBAvailability))
	values.Add("force_get", toIntBoolArg(req.ForceGet))
	values.Add("skip_first_archive", toIntBoolArg(req.SkipFirstArchive))
	values.Add("outlinks_availability", toIntBoolArg(req.OutlinksAvailability))
	values.Add("email_result", toIntBoolArg(req.EmailResult))

	if req.CaptureCookie != "" {
		values.Add("capture_cookie", req.CaptureCookie)
	}

	if req.UserAgent != "" {
		values.Add("use_user_agent", req.UserAgent)
	}

	if req.TargetUsername != "" {
		values.Add("target_username", req.TargetUsername)
	}

	if req.TargetPassword != "" {
		values.Add("target_password", req.TargetPassword)
	}

	if req.IfNotArchivedWithinSecs > 0 {
		values.Add("if_not_archived_within",
			fmt.Sprintf("%d", req.IfNotArchivedWithinSecs))
	}

	if req.DelayForJavascript && req.JavascriptTimeout > 0 {
		values.Add("js_behavior_timeout", fmt.Sprintf("%d", req.JavascriptTimeout))
	}

	params := values.Encode()

	if c.Debug {
		fmt.Printf("-> %s\n", params)
	}

	err := c.do("POST", "", bytes.NewBufferString(params), &out)
	if err != nil {
		return out, fmt.Errorf("capture request failed: %w", err)
	}

	return out, nil
}

type TestClient struct {
	// TODO
}

// NewDefaultClient creates an SPN DefaultClient. It accepts configuration that
// must include an access key and an access secret; endpoint is optional.
func NewDefaultClient(cfg SPNConfig) (Client, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "web.archive.org/save"
	}
	if cfg.AccessKey == "" {
		return nil, errors.New("cannot create spn client without access key")
	}
	if cfg.SecretKey == "" {
		return nil, errors.New("cannot create spn client without secret key")
	}
	return &DefaultClient{
		Config: cfg,
		client: &http.Client{},
		Debug:  cfg.Debug,
	}, nil
}
