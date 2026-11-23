package fatcat2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/viper"
)

// UpdateRelease replaces a release in fc2 wholesale with r (PUT /release).
//
// Wholesale means every child list r leaves empty is emptied on the server. Only
// call this with a Release obtained from GetRelease and then modified; see
// roundtrip_test.go for why that is safe.
func UpdateRelease(client *http.Client, r Release) error {
	if r.WorkID == nil {
		// fc2 answers 422 for this, but the message is easy to misread as a
		// problem with the payload rather than with how it was obtained.
		return fmt.Errorf("release %s has no work_id; update only releases fetched with GetRelease", r.ID)
	}
	bs, err := json.Marshal(&r)
	if err != nil {
		return fmt.Errorf("release marshal failed: %w", err)
	}
	if len(bs) > maxReleasePayloadBytes {
		return fmt.Errorf("release payload too large: %d bytes (max %d)", len(bs), maxReleasePayloadBytes)
	}
	return put(client, "/release", r.ID.String(), bs)
}

// UpdateFile replaces a file in fc2 wholesale with f (PUT /file), including its
// URLs and the releases it is linked to. As with UpdateRelease, only call this
// with a File obtained from GetFile and then modified.
func UpdateFile(client *http.Client, f File) error {
	bs, err := json.Marshal(&f)
	if err != nil {
		return fmt.Errorf("file marshal failed: %w", err)
	}
	return put(client, "/file", f.ID.String(), bs)
}

func put(client *http.Client, path, id string, body []byte) error {
	req, err := http.NewRequest("PUT", viper.GetString("fatcat2.endpoint")+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not build PUT %s request: %w", path, err)
	}
	req.Header.Set("X-API-Key", viper.GetString("fatcat2.key"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s failed for %s: %w", path, id, err)
	}
	defer resp.Body.Close()

	// 201 would mean the entity did not exist and the PUT created it, which for
	// an update means we were handed something other than a fetched entity.
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code for %s PUT %s: %d; body '%s'", path, id, resp.StatusCode, b)
	}
	return nil
}
