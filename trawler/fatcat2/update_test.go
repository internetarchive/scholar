package fatcat2

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateReleaseAndFile(t *testing.T) {
	var gotPath, gotMethod, gotKey string
	var gotBody map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotKey = r.URL.Path, r.Method, r.Header.Get("X-API-Key")
		bs, _ := io.ReadAll(r.Body)
		gotBody = nil
		json.Unmarshal(bs, &gotBody)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	viper.Set("fatcat2.endpoint", srv.URL)
	viper.Set("fatcat2.key", "k")
	t.Cleanup(func() { viper.Set("fatcat2.endpoint", ""); viper.Set("fatcat2.key", "") })

	var rel Release
	require.NoError(t, json.Unmarshal(readFixture(t, "roundtrip_release_citations.json"), &rel))
	require.NoError(t, UpdateRelease(http.DefaultClient, rel))
	assert.Equal(t, "PUT", gotMethod)
	assert.Equal(t, "/release", gotPath)
	assert.Equal(t, "k", gotKey)
	assert.Len(t, gotBody["citations"], 29, "citations travel with the PUT")

	// 201 means the PUT created something, which an update must never do.
	status = http.StatusCreated
	assert.Error(t, UpdateRelease(http.DefaultClient, rel))

	rel.WorkID = nil
	assert.ErrorContains(t, UpdateRelease(http.DefaultClient, rel), "no work_id")

	status = http.StatusOK
	f := File{ID: uuid.New(), Sha1: "x", Extra: map[string]any{"a": "b"}}
	require.NoError(t, UpdateFile(http.DefaultClient, f))
	assert.Equal(t, "/file", gotPath)
	assert.Equal(t, map[string]any{"a": "b"}, gotBody["extra"])
}
