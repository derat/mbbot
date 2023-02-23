// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// editor submits edits to the MusicBrainz website.
type editor struct {
	serverURL string // e.g. "https://musicbrainz.org"
	client    http.Client
	jar       *cookiejar.Jar
	dryRun    bool // if true, don't perform edits
}

func newEditor(ctx context.Context, serverURL, user, pass string) (*editor, error) {
	ed := editor{serverURL: serverURL}

	var err error
	if ed.jar, err = cookiejar.New(nil); err != nil {
		return nil, err
	}

	if err := ed.post(ctx, "/login", map[string]string{
		// TODO: Are csrf_session_key and csrf_token needed here?
		// Login seems to work without them, even on the production site.
		"username":    user,
		"password":    pass,
		"remember_me": "1",
	}); err != nil {
		return nil, err
	}

	return &ed, nil
}

func (ed *editor) post(ctx context.Context, path string, vals map[string]string) error {
	form := make(url.Values)
	for k, v := range vals {
		form.Set(k, v)
	}
	body := strings.NewReader(form.Encode())

	u := ed.serverURL + path

	if ed.dryRun {
		log.Printf("POST %v with body %q", u, form.Encode())
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u, body)
	if err != nil {
		return err
	}
	for _, c := range ed.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}

	resp, err := ed.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ed.jar.SetCookies(resp.Request.URL, resp.Cookies())

	if resp.StatusCode != 200 {
		return fmt.Errorf("got %v: %v", resp.StatusCode, resp.Status)
	}
	return nil
}
