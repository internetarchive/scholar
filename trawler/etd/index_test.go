package etd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedProcessor struct {
	errs  []error
	calls int
}

func (p *scriptedProcessor) Process(context.Context, []byte, string) (pdf.Content, error) {
	p.calls++
	if p.calls <= len(p.errs) {
		return pdf.Content{}, p.errs[p.calls-1]
	}
	return pdf.Content{GrobidXML: []byte("<TEI/>")}, nil
}

func busy() error { return &pdf.GrobidUnexpectedError{StatusCode: http.StatusServiceUnavailable} }

func TestProcessWhenFree(t *testing.T) {
	ms := []time.Duration{time.Millisecond, time.Millisecond}

	p := &scriptedProcessor{errs: []error{busy(), busy()}}
	c, err := esIndexer{processor: p, busyBackoff: ms}.processWhenFree(context.Background(), nil)
	require.NoError(t, err, "503s within the budget are waited out")
	assert.NotEmpty(t, c.GrobidXML)
	assert.Equal(t, 3, p.calls)

	p = &scriptedProcessor{errs: []error{busy(), busy(), busy()}}
	_, err = esIndexer{processor: p, busyBackoff: ms}.processWhenFree(context.Background(), nil)
	assert.Error(t, err, "past the budget the 503 is the job's failure")
	assert.Equal(t, 3, p.calls)

	p = &scriptedProcessor{errs: []error{&pdf.GrobidUnexpectedError{StatusCode: 500}}}
	_, err = esIndexer{processor: p, busyBackoff: ms}.processWhenFree(context.Background(), nil)
	assert.Error(t, err)
	assert.Equal(t, 1, p.calls, "only a 503 means busy")
}
