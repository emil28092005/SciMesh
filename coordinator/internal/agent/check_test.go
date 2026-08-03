package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckAuthAcceptsToken(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks/claim" && r.Header.Get("Authorization") == "Bearer good-token" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stub.Close()

	item := CheckAuth(context.Background(), stub.URL, "good-token", "", "")
	if !item.OK {
		t.Errorf("good token: %+v", item)
	}
	item = CheckAuth(context.Background(), stub.URL, "bad-token", "", "")
	if item.OK {
		t.Error("bad token must fail")
	}
}

func TestCheckAuthNoCredentialIsNotAFailure(t *testing.T) {
	item := CheckAuth(context.Background(), "http://coord:8080", "", "", "")
	if !item.OK {
		t.Errorf("no credential must not fail the preflight: %+v", item)
	}
}

func TestCheckAuthWorkerKeyExchange(t *testing.T) {
	var exchanged bool
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/worker-tokens/exchange" {
			t.Errorf("unexpected userservice path %q", r.URL.Path)
		}
		exchanged = true
		_, _ = w.Write([]byte(`{"token":"jwt-after-exchange"}`))
	}))
	defer users.Close()
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer jwt-after-exchange" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer coord.Close()

	item := CheckAuth(context.Background(), coord.URL, "", "smk_key", users.URL)
	if !item.OK || !exchanged {
		t.Errorf("key exchange flow: %+v exchanged=%v", item, exchanged)
	}

	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer rejected.Close()
	if item := CheckAuth(context.Background(), coord.URL, "", "smk_bad", rejected.URL); item.OK {
		t.Error("rejected key must fail the preflight")
	}
}
