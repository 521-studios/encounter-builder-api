package svctoken

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSource_DiscoversPostsAndCaches(t *testing.T) {
	var posts int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": srv.URL + "/oauth/token"})
		case "/oauth/token":
			posts++
			_ = r.ParseForm()
			if r.Header.Get("Authorization") != "" {
				t.Error("client_secret_post: no Authorization header expected")
			}
			for k, want := range map[string]string{"grant_type": "client_credentials", "scope": "lr:eb-push", "client_id": "encounter-builder-test", "client_secret": "s3cret"} {
				if got := r.PostForm.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "token_type": "Bearer", "expires_in": 300})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := New(srv.URL+"/", "encounter-builder-test", "s3cret", "lr:eb-push")
	clock := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return clock }
	for range 2 {
		if tok, err := s.Token(context.Background()); err != nil || tok != "tok" {
			t.Fatalf("Token = %q, %v", tok, err)
		}
	}
	if posts != 1 {
		t.Fatalf("posts = %d, want 1 (cached)", posts)
	}
	clock = clock.Add(241 * time.Second) // within a minute of the 300 s expiry
	if _, err := s.Token(context.Background()); err != nil || posts != 2 {
		t.Fatalf("near expiry: posts = %d, err = %v; want a refetch", posts, err)
	}
}

func TestSource_ErrorsAreErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"rejected": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/token" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": "http://" + r.Host + "/oauth/token"})
		},
		"no token": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/token" {
				_ = json.NewEncoder(w).Encode(map[string]any{"expires_in": 300})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": "http://" + r.Host + "/oauth/token"})
		},
		"no discovery": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"token_endpoint on another host": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": "https://evil.test/oauth/token"})
		},
		"redirected token post": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/token" {
				http.Redirect(w, r, "https://evil.test/steal", http.StatusTemporaryRedirect)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": "http://" + r.Host + "/oauth/token"})
		},
	} {
		srv := httptest.NewServer(h)
		if _, err := New(srv.URL, "c", "s", "lr:eb-push").Token(context.Background()); err == nil {
			t.Errorf("%s: want an error", name)
		}
		srv.Close()
	}
}
