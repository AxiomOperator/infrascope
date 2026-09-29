package ghupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/henrygd/beszel"
)

func newTestKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(public), private
}

// newSignedReleaseServer serves a release with one archive and, unless
// signature is empty, its signature asset.
func newSignedReleaseServer(t *testing.T, archiveName string, archive []byte, signature string) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(archive)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case latestPath:
			assets := fmt.Sprintf(`{"name":%q,"browser_download_url":"%s/archive","digest":"sha256:%s"}`, archiveName, srv.URL, hex.EncodeToString(digest[:]))
			if signature != "" {
				assets += fmt.Sprintf(`,{"name":%q,"browser_download_url":"%s/signature"}`, archiveName+SignatureSuffix, srv.URL)
			}
			_, _ = fmt.Fprintf(w, `{"tag_name":"v999.0.0","assets":[%s]}`, assets)
		case "/archive":
			_, _ = w.Write(archive)
		case "/signature":
			_, _ = w.Write([]byte(signature))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, srv *httptest.Server, publicKey string) *updater {
	t.Helper()
	return &updater{
		currentVersion: beszel.Version,
		config: Config{
			APIBaseURL:        srv.URL,
			ArchiveExecutable: "beszel",
			Context:           context.Background(),
			HttpClient:        srv.Client(),
			DataDir:           t.TempDir(),
			PublicKey:         publicKey,
		},
	}
}

func TestSignArchiveRoundTrip(t *testing.T) {
	publicKey, privateKey := newTestKey(t)
	key, err := ParsePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/archive"
	if err := os.WriteFile(path, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	signature := SignArchive(privateKey, []byte("release"))
	if err := verifyArchiveSignature(key, path, []byte(signature)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveSignature(key, path, []byte(signature)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered archive accepted: %v", err)
	}
	if err := verifyArchiveSignature(key, path, []byte("not base64!")); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("malformed signature accepted: %v", err)
	}

	// The signing tool accepts the seed or the full private key.
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(privateKey.Seed()),
		base64.StdEncoding.EncodeToString(privateKey),
	} {
		parsed, err := ParsePrivateKey(encoded)
		if err != nil || !parsed.Equal(privateKey) {
			t.Fatalf("ParsePrivateKey() = %v, %v", parsed, err)
		}
	}
}

func TestUpdateFailsClosedWithoutEmbeddedKey(t *testing.T) {
	srv := newSignedReleaseServer(t, "beszel_linux_amd64.tar.gz", []byte("archive"), "")
	_, err := testUpdater(t, srv, "").update()
	if !errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("expected ErrUnsignedBuild, got %v", err)
	}
	if !strings.Contains(err.Error(), "signing key") {
		t.Fatalf("error should explain that releases must be signed: %v", err)
	}
}

func TestDownloadVerifiedAsset(t *testing.T) {
	publicKey, privateKey := newTestKey(t)
	key, err := ParsePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey := newTestKey(t)
	const name = "beszel_linux_amd64.tar.gz"
	archive := []byte("release archive")

	testCases := []struct {
		name      string
		signature string
		wantErr   error
	}{
		{"valid signature", SignArchive(privateKey, archive), nil},
		{"missing signature asset", "", ErrInvalidSignature},
		{"signed by another key", SignArchive(otherKey, archive), ErrInvalidSignature},
		{"signature of another archive", SignArchive(privateKey, []byte("other")), ErrInvalidSignature},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSignedReleaseServer(t, name, archive, tc.signature)
			p := testUpdater(t, srv, publicKey)
			latest, err := FetchLatestRelease(context.Background(), srv.Client(), LatestReleaseURL(srv.URL, DefaultOwner, DefaultRepo))
			if err != nil {
				t.Fatal(err)
			}
			_, path, err := p.downloadVerifiedAsset(latest, name, key, t.TempDir())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if data, _ := os.ReadFile(path); string(data) != string(archive) {
					t.Fatal("archive not downloaded")
				}
			}
		})
	}
}
