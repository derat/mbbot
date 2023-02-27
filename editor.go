// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

const sessionCookie = "musicbrainz_server_session"

// editor submits edits to the MusicBrainz website.
type editor struct {
	serverURL    string // e.g. "https://musicbrainz.org"
	client       http.Client
	jar          *cookiejar.Jar
	dryRun       bool           // if true, don't perform edits
	editIDRegexp *regexp.Regexp // matches ID in <server>/edit/<id> URLs
}

var (
	// These fragile regexps are used to extract hidden inputs from the login form.
	csrfSessionKeyRegexp = regexp.MustCompile(`<input name="csrf_session_key"\s+type="hidden"\s+value="([^"]+)"`)
	csrfTokenRegexp      = regexp.MustCompile(`<input name="csrf_token"\s+type="hidden"\s+value="([^"]+)"`)
)

func newEditor(ctx context.Context, serverURL, user, pass string) (*editor, error) {
	ed := editor{
		serverURL:    serverURL,
		editIDRegexp: regexp.MustCompile(regexp.QuoteMeta(serverURL) + `/edit/(\d+)\b`),
	}

	var err error
	if ed.jar, err = cookiejar.New(nil); err != nil {
		return nil, err
	}

	// We need to extract a few hidden CSRF-related inputs from the login form to avoid a
	// "The form you’ve submitted has expired. Please resubmit your request." error.
	b, err := ed.get(ctx, "/login")
	if err != nil {
		return nil, err
	}
	var csrfSessionKey string
	if ms := csrfSessionKeyRegexp.FindStringSubmatch(string(b)); ms == nil {
		return nil, errors.New("didn't find csrf_session_key input")
	} else {
		csrfSessionKey = ms[1]
	}
	var csrfToken string
	if ms := csrfTokenRegexp.FindStringSubmatch(string(b)); ms == nil {
		return nil, errors.New("didn't find csrf_token input")
	} else {
		csrfToken = ms[1]
	}

	if b, err = ed.post(ctx, "/login", map[string]string{
		"csrf_session_key": csrfSessionKey,
		"csrf_token":       csrfToken,
		"username":         user,
		"password":         pass,
		"remember_me":      "1",
	}); err != nil {
		return nil, err
	}

	// The server looks like it sets the musicbrainz_server_session cookie on all requests, even
	// when we're not logged in, so look for an error message to check if login failed (and then
	// double-check that there's a link to the user's profile page, just in case the error message
	// changes or is different due to i18n or whatever).
	if strings.Contains(string(b), "Incorrect username or password") {
		return nil, errors.New("incorrect username or password")
	} else if !strings.Contains(string(b), `<a href="/user/`+user+`">`) {
		fmt.Println(string(b))
		return nil, errors.New("missing profile link")
	}

	return &ed, nil
}

// get sends a GET request for path and returns the response body.
func (ed *editor) get(ctx context.Context, path string) ([]byte, error) {
	return ed.send(ctx, http.MethodGet, path, nil)
}

// post sends a POST request for path with the supplied URL-encoded parameters in the body
// and returns the response body.
func (ed *editor) post(ctx context.Context, path string, vals map[string]string) ([]byte, error) {
	return ed.send(ctx, http.MethodPost, path, vals)
}

// send sends a request for path with the supplied URL-encoded parameters as a body.
// The response body is returned. All non-200 responses (after following redirects)
// cause an error to be returned.
func (ed *editor) send(ctx context.Context, method, path string, vals map[string]string) ([]byte, error) {
	u := ed.serverURL + path

	var body io.Reader
	if method == http.MethodPost {
		form := make(url.Values)
		for k, v := range vals {
			form.Set(k, v)
		}
		if ed.dryRun {
			log.Printf("POST %v with body %q", u, form.Encode())
			return []byte(ed.serverURL + "/edit/0"), nil // matched by editIDRegexp
		}
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for _, c := range ed.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", ed.serverURL)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := ed.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	ed.jar.SetCookies(resp.Request.URL, resp.Cookies())

	b, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return b, fmt.Errorf("got %v: %v", resp.StatusCode, resp.Status)
	}
	return b, err
}
