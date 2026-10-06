package fatcat2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// containerLookupServer answers fc2 container lookups from existing, keyed
// "id_type=id_value", and records every lookup it is asked.
func containerLookupServer(t *testing.T, existing map[string]uuid.UUID, status int) *[]string {
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("id_type") + "=" + r.URL.Query().Get("id_value")
		asked = append(asked, key)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		id, ok := existing[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id})
	}))
	t.Cleanup(srv.Close)
	viper.Set("fatcat2.endpoint", srv.URL)
	return &asked
}

func Test_LookupContainerByISSNs_TriesEveryColumn(t *testing.T) {
	id := uuid.New()
	// A container created without an ISSN-L, findable only by its print ISSN.
	asked := containerLookupServer(t, map[string]uuid.UUID{"issnp=3105-4242": id}, 0)

	got, err := LookupContainerByISSNs(http.DefaultClient, "", "1881-3933", "1881-3933", "3105-4242")
	require.NoError(t, err)
	require.Equal(t, &id, got)
	// Empty and repeated ISSNs are passed over; it stops at the first hit.
	require.Equal(t, []string{
		"issnl=1881-3933", "issnp=1881-3933", "issne=1881-3933",
		"issnl=3105-4242", "issnp=3105-4242",
	}, *asked)
}

func Test_LookupContainerByISSNs_NotFound(t *testing.T) {
	asked := containerLookupServer(t, map[string]uuid.UUID{}, 0)

	got, err := LookupContainerByISSNs(http.DefaultClient, "3105-4242")
	require.NoError(t, err)
	require.Nil(t, got)
	require.Len(t, *asked, 3)
}

// Nothing to look up makes no requests at all, which is what an importer with
// no valid ISSNs hands it.
func Test_LookupContainerByISSNs_Nothing(t *testing.T) {
	asked := containerLookupServer(t, map[string]uuid.UUID{}, 0)

	got, err := LookupContainerByISSNs(http.DefaultClient, "", "")
	require.NoError(t, err)
	require.Nil(t, got)
	require.Empty(t, *asked)
}

func Test_LookupContainerByISSNs_Error(t *testing.T) {
	containerLookupServer(t, nil, 500)

	_, err := LookupContainerByISSNs(http.DefaultClient, "3105-4242")
	require.Error(t, err)
}
