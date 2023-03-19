// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	ed  *editor

	mbidURLs map[string]string // MBID-to-URL mappings to return
	mbidRels map[string][]jsonRelationship
	requests []request // POST requests sent to server

	origLogDest io.Writer
}

func newTestEnv(ctx context.Context, t *testing.T) *testEnv {
	env := testEnv{
		t:           t,
		mux:         http.NewServeMux(),
		mbidURLs:    make(map[string]string),
		mbidRels:    make(map[string][]jsonRelationship),
		origLogDest: log.Writer(),
	}

	// Hide spammy logs.
	log.SetOutput(io.Discard)

	env.mux.HandleFunc("/login", env.handleLogin)
	env.mux.HandleFunc("/", env.handleDefault)

	env.srv = httptest.NewServer(env.mux)
	toClose := &env
	defer func() {
		if toClose != nil {
			toClose.close()
		}
	}()

	var err error
	env.ed, err = newEditor(ctx, env.srv.URL, testUser, testPass, editorRateLimit(rate.Inf))
	if err != nil {
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
	switch req.Method {
	case http.MethodGet:
		env.handleGet(w, req)
	case http.MethodPost:
		env.handlePost(w, req)
	default:
		env.t.Errorf("Unexpected %v request for %v", req.Method, req.URL.Path)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (env *testEnv) handleGet(w http.ResponseWriter, req *http.Request) {
	if ms := editURLPathRegexp.FindStringSubmatch(req.URL.Path); ms != nil {
		mbid := ms[1]
		url, ok := env.mbidURLs[mbid]
		if !ok {
			http.NotFound(w, req)
			return
		}

		var data jsonData
		data.Stash.SourceEntity.Name = url
		data.Stash.SourceEntity.Decoded = url
		data.Stash.SourceEntity.Relationships = env.mbidRels[mbid]

		io.WriteString(w, `<!DOCTYPE html><html><head>`)
		io.WriteString(w, `<script>Object.defineProperty(window,"__MB__",{value:Object.freeze({"DBDefs":Object.freeze({}),"$c":Object.freeze(`)
		json.NewEncoder(w).Encode(data)
		io.WriteString(w, `)})})</script></head></html>`)
	} else {
		http.NotFound(w, req)
	}
}

func (env *testEnv) handlePost(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		env.t.Errorf("Failed parsing request for %v: %v", req.URL.Path, err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	u := req.URL
	u.Scheme = ""
	u.Host = ""
	env.requests = append(env.requests, request{u.String(), req.PostForm})

	switch {
	case cancelEditPathRegexp.MatchString(req.URL.Path):
		// TODO: Maybe return something here? The bot doesn't check the response.
	case editURLPathRegexp.MatchString(req.URL.Path):
		// Write a simple page containing an arbitrary edit ID.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
  <head><title>MusicBrainz</title></head>
  <body>
    <p>Thank you, your <a href="%s/edit/123">edit</a> (#123) has been entered into the edit queue for peer review.</p>
  </body>
</html>`, env.srv.URL)
	case req.URL.Path == "/relationship-editor":
		// Just write a bogus JSON object reporting one successful edit.
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"edits":[{"edit_type":1,"response":1}]}`)
	default:
		env.t.Errorf("Unexpected post to %v", req.URL.Path)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	}
}

var (
	cancelEditPathRegexp = regexp.MustCompile(`^/edit/\d+/cancel$`)
	editURLPathRegexp    = regexp.MustCompile(`^/url/([^/]+)/edit$`)
)

// request describes a request that was posted to the server.
type request struct {
	path   string
	params url.Values
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
	want := []request{{
		path: fmt.Sprintf("/edit/%d/cancel", id),
		params: url.Values{
			"confirm.edit_note": []string{editNote},
		},
	}}
	if diff := cmp.Diff(want, env.requests, cmp.AllowUnexported(request{})); diff != "" {
		t.Error("Bad requests:\n" + diff)
	}
}

func TestUpdateURL(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(ctx, t)
	defer env.close()

	const (
		tidalMBID      = "40d2c699-f615-4f95-b212-24c344572333"
		geocitiesMBID  = "56313079-1796-4fb8-add5-d8cf117f3ba5"
		tidalStoreMBID = "545eb1f2-630f-47ff-ad38-9b15e7c0cae9"
		recmusicMBID   = "4e135691-fdc1-4127-ab69-67095aa09c44"
		doneMBID       = "e9ce6782-29e6-4f09-82b0-0abd18061e32"

		recmusicArtistMBID = "63a5c79f-697e-47e0-975d-1e2087a454aa"
	)

	env.mbidURLs[tidalMBID] = "http://listen.tidal.com/artist/11069"
	env.mbidURLs[geocitiesMBID] = "http://www.geocities.com/user"
	env.mbidURLs[tidalStoreMBID] = "https://store.tidal.com/artist/12345"
	env.mbidURLs[recmusicMBID] = "https://recmusic.jp/album/?id=1010526534"
	env.mbidURLs[doneMBID] = "https://tidal.com/album/1234" // already normalized

	env.mbidRels[geocitiesMBID] = []jsonRelationship{
		{ID: 123, LinkTypeID: 3},
		{ID: 456, LinkTypeID: 7, BeginDate: jsonDate{2000, 4, 5}},
	}
	env.mbidRels[tidalStoreMBID] = []jsonRelationship{
		{ID: 789, LinkTypeID: 85, Target: jsonTarget{EntityType: "release"}, Backward: true},
	}
	env.mbidRels[recmusicMBID] = []jsonRelationship{
		{ID: 423, LinkTypeID: 980, Target: jsonTarget{EntityType: "release", GID: recmusicArtistMBID}, Backward: true},
	}

	for _, mbid := range []string{tidalMBID, geocitiesMBID, tidalStoreMBID, recmusicMBID, doneMBID} {
		if err := updateURL(ctx, env.ed, mbid, "", false); err != nil {
			t.Errorf("updateURL(ctx, ed, %q, %q, false) failed: %v", mbid, "", err)
		}
	}
	want := []request{
		{
			path: "/url/" + tidalMBID + "/edit",
			params: makeURLValues(map[string]string{
				"edit-url.url":       "https://tidal.com/artist/11069",
				"edit-url.edit_note": tidalEditNote,
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    geocitiesEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "123",
				"rel-editor.rels.0.link_type":             "3",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "26",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2009",
				"rel-editor.rels.1.action":                "edit",
				"rel-editor.rels.1.id":                    "456",
				"rel-editor.rels.1.link_type":             "7",
				"rel-editor.rels.1.period.ended":          "1",
				"rel-editor.rels.1.period.end_date.day":   "26",
				"rel-editor.rels.1.period.end_date.month": "10",
				"rel-editor.rels.1.period.end_date.year":  "2009",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    tidalStoreEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "789",
				"rel-editor.rels.0.link_type":             "74",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "20",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2022",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    recmusicEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "423",
				"rel-editor.rels.0.link_type":             "980",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "1",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2021",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                      recmusicEditNote,
				"rel-editor.rels.0.action":                  "add",
				"rel-editor.rels.0.link_type":               "980",
				"rel-editor.rels.0.period.begin_date.day":   "1",
				"rel-editor.rels.0.period.begin_date.month": "10",
				"rel-editor.rels.0.period.begin_date.year":  "2021",
				"rel-editor.rels.0.entity.0.gid":            recmusicArtistMBID,
				"rel-editor.rels.0.entity.0.type":           "release",
				"rel-editor.rels.0.entity.1.url":            "https://music.tower.jp/album/detail/1010526534",
				"rel-editor.rels.0.entity.1.type":           "url",
			}),
		},
	}
	if diff := cmp.Diff(want, env.requests, cmp.AllowUnexported(request{})); diff != "" {
		t.Error("Bad requests:\n" + diff)
	}
}

func makeURLValues(m map[string]string) url.Values {
	vals := make(url.Values)
	for k, v := range m {
		vals.Set(k, v)
	}
	return vals
}
