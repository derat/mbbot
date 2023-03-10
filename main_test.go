// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/time/rate"
)

const (
	testUser           = "someuser"
	testPass           = "secret123"
	testSession        = "67d6e3af345531d14024e065dda8edc762c62bfd"
	testCSRFSessionKey = "csrf_token:yDxVoERSSn3myMFAXK0obEZaJRjliGnPtp+Cyfz5Eek="
	testCSRFToken      = "WX6VYHNb7TEaBTgPwLjU9jkJS4/TpJu/b6EKrIpK+n0="
)

type testEnv struct {
	t   *testing.T
	srv *httptest.Server
	mux *http.ServeMux
	api *api
	ed  *editor

	mbidURLs map[string]string // MBID-to-URL mappings for API
	requests []request         // POST requests sent to server

	origLogDest io.Writer
}

func newTestEnv(ctx context.Context, t *testing.T) *testEnv {
	env := testEnv{
		t:           t,
		mux:         http.NewServeMux(),
		mbidURLs:    make(map[string]string),
		origLogDest: log.Writer(),
	}

	// Hide spammy logs.
	log.SetOutput(io.Discard)

	env.mux.HandleFunc("/login", env.handleLogin)
	env.mux.HandleFunc("/ws/2/url/", env.handleAPIURL)
	env.mux.HandleFunc("/", env.handleDefault)

	env.srv = httptest.NewServer(env.mux)
	toClose := &env
	defer func() {
		if toClose != nil {
			toClose.close()
		}
	}()

	env.api = newAPI(env.srv.URL)
	env.api.limiter.SetLimit(rate.Inf)

	var err error
	if env.ed, err = newEditor(ctx, env.srv.URL, testUser, testPass); err != nil {
		t.Fatal("Failed logging in:", err)
	}

	toClose = nil // disarm cleanup
	return &env
}

func (env *testEnv) close() {
	env.srv.Close()
	log.SetOutput(env.origLogDest)
}

func (env *testEnv) handleLogin(w http.ResponseWriter, req *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: testSession,
		Path:  "/",
	})

	switch req.Method {
	case http.MethodGet:
		// Return a minimal page with a form with CSRF-related inputs.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
  <head><title>MusicBrainz</title></head>
  <body>
    <form action="/login" method="post">
      <input name="csrf_session_key" type="hidden" value="%s"/>
      <input name="csrf_token" type="hidden" value="%s"/>
    </form>
  </body>
</html>`, testCSRFSessionKey, testCSRFToken)

	case http.MethodPost:
		user := req.FormValue("username")
		pass := req.FormValue("password")
		if user != testUser || pass != testPass {
			env.t.Errorf("Login attempt as %v/%v; want %v/%v", user, pass, testUser, testPass)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		if v := req.FormValue("csrf_session_key"); v != testCSRFSessionKey {
			env.t.Errorf("Login attempt with csrf_session_key %q; want %q", v, testCSRFSessionKey)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if v := req.FormValue("csrf_token"); v != testCSRFToken {
			env.t.Errorf("Login attempt with csrf_token %q; want %q", v, testCSRFToken)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		// Return a minimal page with the profile link that the editor code looks
		// for to check whether login was successful.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
  <head><title>MusicBrainz</title></head>
  <body><a href="/user/%s">Profile</a></body>
</html>`, testUser)

	default:
		env.t.Errorf("Unexpected %v login request", req.Method)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (env *testEnv) handleDefault(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		env.t.Errorf("Unexpected %v request for %v", req.Method, req.URL.Path)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if err := req.ParseForm(); err != nil {
		env.t.Errorf("Failed parsing request for %v: %v", req.URL.Path, err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	u := req.URL
	u.Scheme = ""
	u.Host = ""
	env.requests = append(env.requests, request{u.String(), req.PostForm})

	// Write a simple page containing an arbitrary edit ID.
	// TODO: Maybe return something different for cancel requests? It doesn't matter at the moment.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
  <head><title>MusicBrainz</title></head>
  <body>
    <p>Thank you, your <a href="%s/edit/123">edit</a> (#123) has been entered into the edit queue for peer review.</p>
  </body>
</html>`, env.srv.URL)
}

func (env *testEnv) handleAPIURL(w http.ResponseWriter, req *http.Request) {
	mbid := strings.TrimPrefix(req.URL.Path, "/ws/2/url/")
	url, ok := env.mbidURLs[mbid]
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://musicbrainz.org/ns/mmd-2.0#">
  <url id="%s">
    <resource>%s</resource>
  </url>
</metadata>`, mbid, url)
}

// request describes a request that was posted to the server.
type request struct {
	path   string
	params url.Values
}

// cancelRequest constructs a request for canceling an edit.
func cancelRequest(id int, editNote string) request {
	return request{
		path: fmt.Sprintf("/edit/%d/cancel", id),
		params: url.Values{
			"confirm.edit_note": []string{editNote},
		},
	}
}

// urlRequest constructs a request for changing a URL.
func urlRequest(mbid, newURL, editNote string) request {
	return request{
		path: "/url/" + mbid + "/edit",
		params: url.Values{
			"edit-url.url":       []string{newURL},
			"edit-url.edit_note": []string{editNote},
		},
	}
}

func TestCancelEdit(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(ctx, t)
	defer env.close()

	const (
		id       = 123
		editNote = "testing edit cancelation"
	)
	if err := cancelEdit(ctx, env.ed, id, editNote); err != nil {
		t.Fatalf("cancelEdit(ctx, ed, %d, %q) failed: %v", id, editNote, err)
	}
	want := []request{cancelRequest(id, editNote)}
	if diff := cmp.Diff(want, env.requests, cmp.AllowUnexported(request{})); diff != "" {
		t.Error("Bad requests:\n" + diff)
	}
}

func TestRewriteURL(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(ctx, t)
	defer env.close()

	const (
		mbid1 = "40d2c699-f615-4f95-b212-24c344572333"
		mbid2 = "56313079-1796-4fb8-add5-d8cf117f3ba5"
		mbid3 = "e9ce6782-29e6-4f09-82b0-0abd18061e32"
	)

	env.mbidURLs[mbid1] = "http://listen.tidal.com/artist/11069"
	env.mbidURLs[mbid2] = "https://tidal.com/browse/album/11103031"
	env.mbidURLs[mbid3] = "https://tidal.com/album/1234" // already normalized

	for _, mbid := range []string{mbid1, mbid2, mbid3} {
		if err := rewriteURL(ctx, env.api, env.ed, mbid, ""); err != nil {
			t.Errorf("rewriteURL(ctx, api, ed, %q, %q) failed: %v", mbid, "", err)
		}
	}
	want := []request{
		urlRequest(mbid1, "https://tidal.com/artist/11069", tidalURLEditNote),
		urlRequest(mbid2, "https://tidal.com/album/11103031", tidalURLEditNote),
	}
	if diff := cmp.Diff(want, env.requests, cmp.AllowUnexported(request{})); diff != "" {
		t.Error("Bad requests:\n" + diff)
	}
}

func TestDoRewrite_URL(t *testing.T) {
	for _, tc := range []struct{ orig, want string }{
		{"https://www.example.org/", ""},
		{"https://www.example.org/artist/123", ""},
		{"https://tidal.com/album/11069", ""},      // already canonicalized
		{"https://test.tidal.com/album/11069", ""}, // unknown hostname
		{"http://www.tidal.com/test/11069", ""},    // unknown path component
		{"http://tidal.com/album/11069", "https://tidal.com/album/11069"},
		{"https://listen.tidal.com/artist/11069", "https://tidal.com/artist/11069"},
		{"https://tidal.com/browse/track/11069", "https://tidal.com/track/11069"},
		{"https://www.tidal.com/album/11069", "https://tidal.com/album/11069"},
		{"https://listen.tidal.com/album/143547274/track/143547275", "https://tidal.com/track/143547275"},
		{"https://desktop.tidal.com/album/163812859", "https://tidal.com/album/163812859"},
		{"http://tidal.com/browse/album/119425271?play=true", "https://tidal.com/album/119425271"},
		{"https://tidal.com/browse/album/126495793/", "https://tidal.com/album/126495793"},
		{"https://listen.tidal.com/video/78581329", "https://tidal.com/video/78581329"},
		{"https://www.tidal.com/browse/track/155221653", "https://tidal.com/track/155221653"},
	} {
		if res := doRewrite(urlRewrites, tc.orig); res == nil {
			if tc.want != "" {
				t.Errorf("doRewrite(urlRewrites, %q) didn't rewrite; want %q", tc.orig, tc.want)
			}
		} else if res.updated == "" {
			t.Errorf("doRewrite(urlRewrites, %q) rewrote to empty string", tc.orig)
		} else if res.updated != tc.want {
			t.Errorf("doRewrite(urlRewrites, %q) = %q; want %q", tc.orig, res.updated, tc.want)
		}
	}
}
