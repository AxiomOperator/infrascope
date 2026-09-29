//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/henrygd/beszel/internal/ghupdate"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runUpdateCheck(t *testing.T, info *UpdateInfo) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/beszel/update", nil)
	rec := httptest.NewRecorder()
	err := info.getUpdate(&core.RequestEvent{Event: router.Event{Request: req, Response: rec}})
	return rec, err
}

func TestGetUpdateQuietWhenNoReleases(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	info := &UpdateInfo{releaseURL: ghupdate.LatestReleaseURL(srv.URL, ghupdate.DefaultOwner, ghupdate.DefaultRepo)}
	rec, err := runUpdateCheck(t, info)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Empty(t, body["v"], "no new version should be reported")
	assert.Empty(t, body["url"])
	assert.Equal(t, 1, hits)

	// cached for 6 hours, so a second call does not hit GitHub again
	_, err = runUpdateCheck(t, info)
	require.NoError(t, err)
	assert.Equal(t, 1, hits)
}

func TestGetUpdateReportsNewerRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/AxiomOperator/infrascope/releases/latest", r.URL.Path)
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","html_url":"https://github.com/AxiomOperator/infrascope/releases/tag/v99.0.0"}`))
	}))
	defer srv.Close()

	info := &UpdateInfo{releaseURL: ghupdate.LatestReleaseURL(srv.URL, ghupdate.DefaultOwner, ghupdate.DefaultRepo)}
	rec, err := runUpdateCheck(t, info)
	require.NoError(t, err)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "99.0.0", body["v"])
	assert.Equal(t, "https://github.com/AxiomOperator/infrascope/releases/tag/v99.0.0", body["url"])
}
