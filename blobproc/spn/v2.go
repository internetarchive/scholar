package spn

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

var ErrMissingAuth = errors.New("missing auth")

type Result struct {
	Success          bool
	Status           string
	JobID            string
	RequestURL       string
	TerminalURL      string
	TerminalDateTime string
	Resources        []string
}

type SaveOpts struct {
	ForceSimpleGet  bool
	CaptureOutlinks bool
}

// Doer is a minimal, local HTTP client abstraction.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client to communicate with save page now.
type Client struct {
	Endpoint       string
	AccessKey      string
	SecretKey      string
	Client         Doer
	PollCount      int
	PollSeconds    time.Duration
	SPNCDXRetrySec time.Duration
	SimpleDomains  []string
}

func (c *Client) Save(link string, opts *SaveOpts) (*Result, error) {
	if c.AccessKey == "" || c.SecretKey == "" {
		return nil, ErrMissingAuth
	}
	if strings.HasPrefix(link, "ftp://") {
		return &Result{
			Success:    false,
			Status:     "spn2-no-ftp",
			RequestURL: link,
		}, nil
	}
	return nil, nil
}
