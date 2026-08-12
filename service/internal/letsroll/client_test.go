package letsroll

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchGame_ForwardsBearerAndParsesGM(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"g1","name":"Rise of the Runelords","am_gm":true}`))
	}))
	defer srv.Close()

	g, err := New(srv.URL).FetchGame(context.Background(), "tok-abc", "g1")
	if err != nil {
		t.Fatalf("FetchGame: %v", err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("Authorization = %q, want bearer forwarded", gotAuth)
	}
	if gotPath != "/api/v1/games/g1" {
		t.Fatalf("path = %q", gotPath)
	}
	if !g.AmGM {
		t.Fatalf("AmGM = false, want true")
	}
}

func TestFetchGame_ForbiddenAndNotFoundCollapse(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		if _, err := New(srv.URL).FetchGame(context.Background(), "t", "g1"); err != ErrForbidden {
			t.Fatalf("status %d: err = %v, want ErrForbidden", code, err)
		}
		srv.Close()
	}
}

func TestFetchGame_UnexpectedStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).FetchGame(context.Background(), "t", "g1"); err == nil || err == ErrForbidden {
		t.Fatalf("err = %v, want a non-forbidden error", err)
	}
}
