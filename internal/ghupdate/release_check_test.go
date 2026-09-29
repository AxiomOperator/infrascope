package ghupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/henrygd/beszel"
)

const latestPath = "/repos/AxiomOperator/infrascope/releases/latest"

func newReleaseServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var gotPath atomic.Value
	gotPath.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotPath
}

func TestLatestReleaseURL(t *testing.T) {
	if got, want := LatestReleaseURL("", DefaultOwner, DefaultRepo), "https://api.github.com"+latestPath; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := LatestReleaseURL("http://example.test/", "o", "r"), "http://example.test/repos/o/r/releases/latest"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFetchLatestReleaseNotFoundReturnsErrNoReleases(t *testing.T) {
	srv, _ := newReleaseServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	_, err := FetchLatestRelease(context.Background(), srv.Client(), LatestReleaseURL(srv.URL, DefaultOwner, DefaultRepo))
	if !errors.Is(err, ErrNoReleases) {
		t.Fatalf("expected ErrNoReleases, got %v", err)
	}
}

func TestFetchLatestReleaseServerError(t *testing.T) {
	srv, _ := newReleaseServer(t, http.StatusInternalServerError, `boom`)
	_, err := FetchLatestRelease(context.Background(), srv.Client(), LatestReleaseURL(srv.URL, DefaultOwner, DefaultRepo))
	if err == nil || errors.Is(err, ErrNoReleases) {
		t.Fatalf("expected a generic error, got %v", err)
	}
}

func TestFetchLatestReleaseParsesRelease(t *testing.T) {
	srv, gotPath := newReleaseServer(t, http.StatusOK, `{"tag_name":"v9.9.9","html_url":"https://github.com/AxiomOperator/infrascope/releases/tag/v9.9.9"}`)
	rel, err := FetchLatestRelease(context.Background(), srv.Client(), LatestReleaseURL(srv.URL, DefaultOwner, DefaultRepo))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v9.9.9" {
		t.Fatalf("unexpected tag %q", rel.Tag)
	}
	if p := gotPath.Load().(string); p != latestPath {
		t.Fatalf("unexpected request path %q", p)
	}
}

func TestUpdateWithoutReleasesIsNoop(t *testing.T) {
	srv, gotPath := newReleaseServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	updated, err := Update(Config{
		ArchiveExecutable: "beszel",
		APIBaseURL:        srv.URL,
		HttpClient:        srv.Client(),
		DataDir:           t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if updated {
		t.Fatal("expected no update")
	}
	if p := gotPath.Load().(string); p != latestPath {
		t.Fatalf("expected default InfraScope repo, requested %q", p)
	}
}

func TestUpdateAlreadyLatest(t *testing.T) {
	srv, _ := newReleaseServer(t, http.StatusOK, `{"tag_name":"v`+beszel.Version+`"}`)
	updated, err := Update(Config{
		ArchiveExecutable: "beszel",
		APIBaseURL:        srv.URL,
		HttpClient:        srv.Client(),
		DataDir:           t.TempDir(),
	})
	if err != nil || updated {
		t.Fatalf("expected no update and no error, got updated=%v err=%v", updated, err)
	}
}
