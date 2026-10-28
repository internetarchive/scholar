// Package containerstest has test doubles for the container importers.
package containerstest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// NewISSN stands in for a journal too new for the ISSN-L table: a well-formed
// ISSN the table does not know. It is drawn from a block (98xx) no ISSN has
// been assigned from, so table updates should not catch up with it;
// Test_NewISSN_NotInTable says so if one does.
const NewISSN = "9876-5434"

// Fatcat stands in for fatcat2 (container lookup and create), fatcat1 (legacy
// lookup) and ES (indexing).
type Fatcat struct {
	mu sync.Mutex
	// existing maps "field=value" to a container ID.
	existing map[string]uuid.UUID
	// Lookups is every container lookup made, as "field=value".
	Lookups []string
	// Created is every container created.
	Created []fatcat2.Container
	// Indexed is how many documents were sent to ES.
	Indexed int
	// Legacy is how many fatcat1 lookups were made.
	Legacy int
}

func (f *Fatcat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == "GET" && r.URL.Path == "/fc2/container/lookup":
		key := r.URL.Query().Get("id_type") + "=" + r.URL.Query().Get("id_value")
		f.Lookups = append(f.Lookups, key)
		id, ok := f.existing[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id})
	case r.Method == "POST" && r.URL.Path == "/fc2/container":
		var c fatcat2.Container
		bs, _ := io.ReadAll(r.Body)
		json.Unmarshal(bs, &c)
		f.Created = append(f.Created, c)
		w.WriteHeader(201)
	case strings.HasPrefix(r.URL.Path, "/fc1/"):
		f.Legacy++
		w.WriteHeader(404)
	case strings.HasPrefix(r.URL.Path, "/es/"):
		f.Indexed++
		w.WriteHeader(201)
	default:
		w.WriteHeader(500)
	}
}

// SetupFatcat starts a fake for the life of the test and points viper's
// fatcat and ES endpoints at it. existing maps "field=value" (e.g.
// "issnl=0915-2245") to the containers fatcat already has.
func SetupFatcat(t *testing.T, existing map[string]uuid.UUID) *Fatcat {
	f := &Fatcat{existing: existing}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	viper.Set("fatcat2.endpoint", srv.URL+"/fc2")
	viper.Set("fatcat1.endpoint", srv.URL+"/fc1")
	viper.Set("indexing.elasticsearch_url", srv.URL+"/es")
	viper.Set("indexing.fatcat_container_ix", "fatcat_container")
	return f
}
