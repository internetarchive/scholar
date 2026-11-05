package fatcat2

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// A container fetched as a doc and put back carries every field fc2 served,
// including ones Container does not know about.
func Test_ContainerDoc_RoundTrip(t *testing.T) {
	id := uuid.New()
	served := map[string]any{
		"id": id.String(), "name": "A Journal", "created": "2019-01-29T00:00:00Z",
		"wikidata_qid": "Q1", "extra": map[string]any{"ezb": map[string]any{"ids": []any{float64(6)}}},
	}
	var put map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/container/"+id.String():
			json.NewEncoder(w).Encode(served)
		case r.Method == "PUT" && r.URL.Path == "/container":
			key = r.Header.Get("X-API-Key")
			bs, _ := io.ReadAll(r.Body)
			json.Unmarshal(bs, &put)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	viper.Set("fatcat2.endpoint", srv.URL)
	viper.Set("fatcat2.key", "sekrit")

	doc, err := GetContainerDoc(http.DefaultClient, id)
	require.NoError(t, err)
	require.Equal(t, served, doc)

	doc["publisher"] = "Pub"
	require.NoError(t, PutContainerDoc(http.DefaultClient, doc))
	require.Equal(t, "sekrit", key)
	require.Equal(t, "Q1", put["wikidata_qid"])
	require.Equal(t, "2019-01-29T00:00:00Z", put["created"])
	require.Equal(t, "Pub", put["publisher"])
}

func Test_ContainerDoc_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	viper.Set("fatcat2.endpoint", srv.URL)

	_, err := GetContainerDoc(http.DefaultClient, uuid.New())
	require.ErrorContains(t, err, "500")

	err = PutContainerDoc(http.DefaultClient, map[string]any{"id": "x"})
	require.ErrorContains(t, err, "500")
}
