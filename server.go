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

	"golang.org/x/time/rate"
)

const (
	// https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
	maxQPS    = 1
	userAgent = "derat_bot/0 ( https://github.com/derat/mbbot )"

	sessionCookie = "musicbrainz_server_session"
)

// server communicates with the MusicBrainz website.
type server struct {
	serverURL    string // e.g. "https://musicbrainz.org"
	client       http.Client
	limiter      *rate.Limiter
	jar          *cookiejar.Jar
	dryRun       bool           // if true, don't perform edits
	editIDRegexp *regexp.Regexp // matches ID in <server>/edit/<id> URLs
}

var (
	// These fragile regexps are used to extract hidden inputs from the login form.
	csrfSessionKeyRegexp = regexp.MustCompile(`<input name="csrf_session_key"\s+type="hidden"\s+value="([^"]+)"`)
	csrfTokenRegexp      = regexp.MustCompile(`<input name="csrf_token"\s+type="hidden"\s+value="([^"]+)"`)
)

type serverOption func(srv *server)

func serverRateLimit(limit rate.Limit) serverOption {
	return func(srv *server) { srv.limiter.SetLimit(limit) }
}
func serverDryRun(dryRun bool) serverOption {
	return func(srv *server) { srv.dryRun = dryRun }
}

func newServer(ctx context.Context, serverURL, user, pass string, opts ...serverOption) (*server, error) {
	// Wait until after login to initialize the rate-limiter.
	srv := server{
		serverURL:    serverURL,
		editIDRegexp: regexp.MustCompile(regexp.QuoteMeta(serverURL) + `/edit/(\d+)\b`),
	}

	var err error
	if srv.jar, err = cookiejar.New(nil); err != nil {
		return nil, err
	}

	// We need to extract a few hidden CSRF-related inputs from the login form to avoid a
	// "The form you’ve submitted has expired. Please resubmit your request." error.
	b, err := srv.get(ctx, "/login")
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

	if b, err = srv.post(ctx, "/login", map[string]string{
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

	// Apply options here so they don't affect login.
	srv.limiter = rate.NewLimiter(maxQPS, 1)
	for _, opt := range opts {
		opt(&srv)
	}

	return &srv, nil
}

// get sends a GET request for path and returns the response body.
func (srv *server) get(ctx context.Context, path string) ([]byte, error) {
	return srv.send(ctx, http.MethodGet, path, nil)
}

// post sends a POST request for path with the supplied URL-encoded parameters in the body
// and returns the response body.
func (srv *server) post(ctx context.Context, path string, vals map[string]string) ([]byte, error) {
	return srv.send(ctx, http.MethodPost, path, vals)
}

// send sends a request for path with the supplied URL-encoded parameters as a body.
// The response body is returned. All non-200 responses (after following redirects)
// cause an error to be returned.
func (srv *server) send(ctx context.Context, method, path string, vals map[string]string) ([]byte, error) {
	if srv.limiter != nil {
		if err := srv.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}

	u := srv.serverURL + path

	var body io.Reader
	if method == http.MethodPost {
		form := make(url.Values)
		for k, v := range vals {
			form.Set(k, v)
		}
		if srv.dryRun {
			log.Printf("POST %v with body %q", u, form.Encode())
			switch {
			case path == "/relationship-editor":
				return []byte(`{"edits":[{"edit_type":1,"response":1}]}`), nil
			case strings.HasSuffix(path, "/edit"):
				return []byte(srv.serverURL + "/edit/0"), nil // matched by editIDRegexp
			}
			return nil, nil
		}
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for _, c := range srv.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", srv.serverURL)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := srv.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	srv.jar.SetCookies(resp.Request.URL, resp.Cookies())

	b, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return b, fmt.Errorf("got %v: %v", resp.StatusCode, resp.Status)
	}
	return b, err
}
