package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNormalizePEP440(t *testing.T) {
	cases := map[string]string{
		"1.1.0":          "1.1.0",
		"1.1.0-alpha.10": "1.1.0a10",
		"1.1.0-beta.2":   "1.1.0b2",
		"1.1.0-rc.1":     "1.1.0rc1",
		"1.0.0":          "1.0.0",
	}
	for in, want := range cases {
		if got := NormalizePEP440(in); got != want {
			t.Errorf("NormalizePEP440(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReleaseWheelURL(t *testing.T) {
	url, name, err := ReleaseWheelURL("1.1.0-alpha.10")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://github.com/emil28092005/SciMesh/releases/download/v1.1.0-alpha.10/scimesh-1.1.0a10-py3-none-any.whl"
	if url != wantURL {
		t.Errorf("url = %q, want %q", url, wantURL)
	}
	if name != "scimesh-1.1.0a10-py3-none-any.whl" {
		t.Errorf("name = %q", name)
	}

	// A dev build has no release wheel.
	if _, _, err := ReleaseWheelURL("dev"); err == nil {
		t.Error("dev build must not resolve a release wheel")
	}
	if _, _, err := ReleaseWheelURL(""); err == nil {
		t.Error("empty version must not resolve a release wheel")
	}
}

func TestDownloadWheel(t *testing.T) {
	payload := []byte("fake wheel bytes")
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer stub.Close()

	dir := t.TempDir()
	path, err := DownloadWheel(context.Background(), stub.URL+"/scimesh-1.1.0a10-py3-none-any.whl", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "scimesh-1.1.0a10-py3-none-any.whl") {
		t.Errorf("path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Error("wheel bytes mismatch")
	}
}

func TestDownloadWheelReportsHTTPErrors(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer stub.Close()
	if _, err := DownloadWheel(context.Background(), stub.URL+"/missing.whl", t.TempDir()); err == nil {
		t.Error("404 must fail the download")
	}
}
