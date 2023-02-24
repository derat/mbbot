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
	edits    []edit

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
	env.edits = append(env.edits, edit{u.String(), req.PostForm})

	// Write a simple page containing an arbitrary edit ID.
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

// edit describes a request that was posted to the server.
type edit struct {
	path   string
	params url.Values
}

// urlEdit constructs an edit for changing a URL.
func urlEdit(mbid, newURL, editNote string) edit {
	return edit{
		path: "/url/" + mbid + "/edit",
		params: url.Values{
			"edit-url.url":       []string{newURL},
			"edit-url.edit_note": []string{editNote},
		},
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
		if err := rewriteURL(ctx, mbid, env.api, env.ed); err != nil {
			t.Errorf("rewriteURL(ctx, %q, api, ed) failed: %v", mbid, err)
		}
	}
	want := []edit{
		urlEdit(mbid1, "https://tidal.com/artist/11069", tidalURLEditNote),
		urlEdit(mbid2, "https://tidal.com/album/11103031", tidalURLEditNote),
	}
	if diff := cmp.Diff(want, env.edits, cmp.AllowUnexported(edit{})); diff != "" {
		t.Error("Bad edits:\n" + diff)
	}
}
