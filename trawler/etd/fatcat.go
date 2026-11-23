package etd

import (
	"net/http"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
)

// Fatcat is the slice of fc2 the metadata side uses. It exists so the line
// logic can be tested against an in-memory fake; fc2API is the real thing.
type Fatcat interface {
	LookupRelease(idType, idValue string) (*uuid.UUID, error)
	GetRelease(id uuid.UUID) (fatcat2.Release, error)
	ReleaseFiles(id uuid.UUID) ([]fatcat2.File, error)
	CreateRelease(r fatcat2.Release) (uuid.UUID, error)
	UpdateRelease(r fatcat2.Release) error

	LookupSha1(sha1 string) (*uuid.UUID, error)
	GetFile(id uuid.UUID) (fatcat2.File, error)
	FileReleases(id uuid.UUID) ([]fatcat2.Release, error)
	CreateFile(f fatcat2.File) (uuid.UUID, error)
	UpdateFile(f fatcat2.File) error
	AddFileURL(id uuid.UUID, u fatcat2.FileURL) (bool, error)
}

type fc2API struct{ c *http.Client }

func (a fc2API) LookupRelease(t, v string) (*uuid.UUID, error) {
	return fatcat2.LookupRelease(a.c, t, v)
}
func (a fc2API) GetRelease(id uuid.UUID) (fatcat2.Release, error) {
	return fatcat2.GetRelease(a.c, id)
}
func (a fc2API) ReleaseFiles(id uuid.UUID) ([]fatcat2.File, error) {
	return fatcat2.ReleaseFiles(a.c, id)
}
func (a fc2API) CreateRelease(r fatcat2.Release) (uuid.UUID, error) {
	id, err := fatcat2.CreateReleaseWithoutExtIDs(a.c, r)
	if err != nil {
		return uuid.Nil, err
	}
	return *id, nil
}
func (a fc2API) UpdateRelease(r fatcat2.Release) error { return fatcat2.UpdateRelease(a.c, r) }
func (a fc2API) LookupSha1(s string) (*uuid.UUID, error) {
	return fatcat2.LookupSha1(a.c, s)
}
func (a fc2API) GetFile(id uuid.UUID) (fatcat2.File, error) { return fatcat2.GetFile(a.c, id) }
func (a fc2API) FileReleases(id uuid.UUID) ([]fatcat2.Release, error) {
	return fatcat2.FileReleases(a.c, id)
}
func (a fc2API) CreateFile(f fatcat2.File) (uuid.UUID, error) {
	id, err := fatcat2.CreateFile(a.c, &f)
	if err != nil {
		return uuid.Nil, err
	}
	return *id, nil
}
func (a fc2API) UpdateFile(f fatcat2.File) error { return fatcat2.UpdateFile(a.c, f) }
func (a fc2API) AddFileURL(id uuid.UUID, u fatcat2.FileURL) (bool, error) {
	return fatcat2.AddFileURL(a.c, id, u)
}
