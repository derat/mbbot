// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// editor communicates with the MusicBrainz website.
type editor struct {
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

type editorOption func(ed *editor)

func editorRateLimit(limit rate.Limit) editorOption {
	return func(ed *editor) { ed.limiter.SetLimit(limit) }
}
func editorDryRun(dryRun bool) editorOption {
	return func(ed *editor) { ed.dryRun = dryRun }
}

func newEditor(ctx context.Context, serverURL, user, pass string, opts ...editorOption) (*editor, error) {
	// Wait until after login to initialize the rate-limiter.
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

	// Apply options here so they don't affect login.
	ed.limiter = rate.NewLimiter(maxQPS, 1)
	for _, opt := range opts {
		opt(&ed)
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
	if ed.limiter != nil {
		if err := ed.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}

	u := ed.serverURL + path

	var body io.Reader
	if method == http.MethodPost {
		form := make(url.Values)
		for k, v := range vals {
			form.Set(k, v)
		}
		if ed.dryRun {
			log.Printf("POST %v with body %q", u, form.Encode())
			switch {
			case path == "/relationship-editor":
				return []byte(`{"edits":[{"edit_type":1,"response":1}]}`), nil
			case strings.HasSuffix(path, "/edit"):
				return []byte(ed.serverURL + "/edit/0"), nil // matched by editIDRegexp
			}
			return nil, nil
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

// urlInfo describes a URL in the database.
type urlInfo struct {
	url  string
	rels []relInfo
}

// relInfo describes a relationship between one entity and another.
type relInfo struct {
	id         int    // database ID of link itself
	linkTypeID int    // database ID of link type, e.g. 978 for artist streaming page
	linkPhrase string // e.g. "has a fan page at"
	beginDate  date
	endDate    date
	ended      bool
	backward   bool
	targetMBID string
	targetName string
	targetType string // entity type, e.g. "artist", "release", "recording"
}

type date struct{ year, month, day int }

func (d *date) empty() bool { return d.year == 0 && d.month == 0 && d.day == 0 }

// desc returns a string describing the relationship belonging to name,
// e.g. "[name] has an official homepage at [url]".
func (rel *relInfo) desc(name string) string {
	target := rel.targetName
	if target == "" {
		target = rel.targetMBID
	}
	phrase := rel.linkPhrase + fmt.Sprintf("[%d]", rel.linkTypeID)

	var s string
	if rel.backward {
		s = fmt.Sprintf("%s %s %s", target, phrase, name)
	} else {
		s = fmt.Sprintf("%s %s %s", name, phrase, target)
	}
	if !rel.beginDate.empty() {
		s += fmt.Sprintf(" from %04d-%02d-%02d", rel.beginDate.year, rel.beginDate.month, rel.beginDate.day)
	}
	if rel.ended {
		s += fmt.Sprintf(" until %04d-%02d-%02d", rel.endDate.year, rel.endDate.month, rel.endDate.day)
	}
	return s
}

// filterRels returns relationships to targets of the specified type, e.g. "artist".
func filterRels(rels []relInfo, entityType string) []relInfo {
	var filtered []relInfo
	for _, rel := range rels {
		if rel.targetType == entityType {
			filtered = append(filtered, rel)
		}
	}
	return filtered
}

// getURLInfo fetches information about a URL (identified by its MBID).
// TODO: Consider updating this to get info about arbitrary entity types.
// Probably the only parts that need to change are the URL path and Decoded field.
func (ed *editor) getURLInfo(ctx context.Context, mbid string) (*urlInfo, error) {
	b, err := ed.get(ctx, "/url/"+mbid+"/edit")
	if err != nil {
		return nil, err
	}

	// This is horrible: extract a property definition from the middle of a script tag.
	seek := func(b []byte, pre string) []byte {
		idx := bytes.Index(b, []byte(pre))
		if idx == -1 {
			return nil
		}
		return b[idx+len(pre):]
	}
	if b = seek(b, `Object.defineProperty(window,"__MB__",`); b == nil {
		return nil, errors.New("missing __MB__ property")
	}
	if b = seek(b, `,"$c":Object.freeze(`); b == nil {
		return nil, errors.New("missing $c property")
	}

	var data jsonData
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&data); err != nil {
		return nil, err
	}
	ent := &data.Stash.SourceEntity
	if ent.Name != ent.Decoded {
		return nil, fmt.Errorf("URLs don't match (name=%q, decoded=%q)", ent.Name, ent.Decoded)
	}
	info := urlInfo{url: ent.Decoded}
	for _, rel := range ent.Relationships {
		info.rels = append(info.rels, rel.toRelInfo())
	}
	return &info, nil
}

// jsonData corresponds to the window.__MB__.$c object.
type jsonData struct {
	Stash struct {
		SourceEntity struct {
			// I have no idea which of this is the canonical URL, so check them both.
			Name    string `json:"name"`
			Decoded string `json:"decoded"`

			Relationships []jsonRelationship `json:"relationships"`
		} `json:"source_entity"`
	} `json:"stash"`
}

// jsonRelationship corresponds to a relationship in jsonData.
type jsonRelationship struct {
	ID            int        `json:"id"`
	LinkTypeID    int        `json:"linkTypeID"`
	Backward      bool       `json:"backward"`
	BeginDate     jsonDate   `json:"begin_date"`
	EndDate       jsonDate   `json:"end_date"`
	Ended         bool       `json:"ended"`
	VerbosePhrase string     `json:"verbosePhrase"`
	Target        jsonTarget `json:"target"`
}

// jsonTarget describes the target entity within jsonRelationship.
type jsonTarget struct {
	Name       string `json:"name"`
	EntityType string `json:"entityType"`
	GID        string `json:"gid"`
}

func (jr *jsonRelationship) toRelInfo() relInfo {
	return relInfo{
		id:         jr.ID,
		linkTypeID: jr.LinkTypeID,
		linkPhrase: jr.VerbosePhrase,
		beginDate:  jr.BeginDate.toDate(),
		endDate:    jr.EndDate.toDate(),
		ended:      jr.Ended,
		backward:   jr.Backward,
		targetMBID: jr.Target.GID,
		targetName: jr.Target.Name,
		targetType: jr.Target.EntityType,
	}
}

// jsonDate holds the individual components of a date.
type jsonDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (jd *jsonDate) toDate() date { return date{jd.Year, jd.Month, jd.Day} }
