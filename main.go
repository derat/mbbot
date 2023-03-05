// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	actionCancel = "cancel" // cancel edits with IDs read from stdin
	actionURLs   = "urls"   // rewrite URLs corresponding to MBIDs read from stdin
)

var allActions = []string{
	actionCancel,
	actionURLs,
}

func main() {
	action := flag.String("action", "", "Action to perform ("+strings.Join(allActions, ", ")+")")
	creds := flag.String("creds", filepath.Join(os.Getenv("HOME"), ".mbbot"), "Path to file containing username and password")
	dryRun := flag.Bool("dry-run", false, "Don't actually perform any edits")
	editNote := flag.String("edit-note", "", "Edit note to attach to all edits")
	server := flag.String("server", "https://test.musicbrainz.org", "Base URL of MusicBrainz server")
	flag.Parse()

	// Validate the action before we bother logging in.
	if *action == "" {
		fmt.Fprintln(os.Stderr, "Must supply action via -action")
		os.Exit(2)
	} else if !sliceContains(allActions, *action) {
		fmt.Fprintf(os.Stderr, "Invalid action %q\n", *action)
		os.Exit(2)
	}

	user, pass, err := readCreds(*creds)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed reading credentials:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	api := newAPI(*server)

	log.Print("Logging in as ", user)
	ed, err := newEditor(ctx, *server, user, pass)
	if err != nil {
		log.Fatal("Failed logging in: ", err)
	}
	ed.dryRun = *dryRun

	switch *action {
	case actionCancel:
		sc := bufio.NewScanner(os.Stdin)
		for {
			if id, err := readInt(sc); err == io.EOF {
				break
			} else if err != nil {
				log.Fatal("Failed reading edit ID: ", err)
			} else if err := cancelEdit(ctx, ed, id, *editNote); err != nil {
				log.Printf("Failed canceling edit %v: %v", id, err)
			}
		}
	case actionURLs:
		sc := bufio.NewScanner(os.Stdin)
		for {
			if mbid, err := readMBID(sc); err == io.EOF {
				break
			} else if err != nil {
				log.Fatal("Failed reading MBID: ", err)
			} else if err := rewriteURL(ctx, api, ed, mbid, *editNote); err != nil {
				log.Printf("Failed rewriting %q: %v", mbid, err)
			}
		}
	}
}

// readCreds reads a whitespace-separated username and password from the file at p.
func readCreds(p string) (user, pass string, err error) {
	b, err := ioutil.ReadFile(p)
	if err != nil {
		return "", "", err
	}
	parts := strings.Fields(string(b))
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected 2 fields; got %v", len(parts))
	}
	return parts[0], parts[1], nil
}

// cancelEdit cancels the MusicBrainz edit with the supplied ID.
func cancelEdit(ctx context.Context, ed *editor, id int, editNote string) error {
	log.Printf("Canceling edit %d", id)
	_, err := ed.post(ctx, fmt.Sprintf("/edit/%d/cancel", id), map[string]string{
		"confirm.edit_note": editNote,
	})
	return err
}

// rewriteURL attempts to rewrite the URL with the specified MBID.
// If editNote is non-empty, it will be attached to the edit.
// If no rewrite is performed, a nil error is returned.
func rewriteURL(ctx context.Context, api *api, ed *editor, mbid, editNote string) error {
	orig, err := api.getURL(ctx, mbid)
	if err != nil {
		return fmt.Errorf("failed getting URL: %v", err)
	}

	var res *rewriteResult
	for re, fn := range urlRewrites {
		if ms := re.FindStringSubmatch(orig); ms != nil {
			res = fn(ms)
			break
		}
	}
	if res == nil {
		log.Printf("%v: no rewrites found for %v", mbid, orig)
		return nil
	}

	if editNote != "" {
		res.editNote = editNote
	}

	log.Printf("%v: rewriting %v to %v", mbid, orig, res.updated)
	b, err := ed.post(ctx, "/url/"+mbid+"/edit", map[string]string{
		"edit-url.url":       res.updated,
		"edit-url.edit_note": res.editNote,
	})
	if err != nil {
		return err
	}
	if ms := ed.editIDRegexp.FindStringSubmatch(string(b)); ms == nil {
		return errors.New("didn't find edit ID")
	} else {
		log.Printf("%v: created edit #%s", mbid, ms[1])
	}
	return nil
}

// rewriteFunc accepts the match groups returned by FindStringSubmatch and returns a non-nil result.
type rewriteFunc func(ms []string) *rewriteResult

type rewriteResult struct {
	updated  string // rewritten string
	editNote string // https://musicbrainz.org/doc/Edit_Note
}

const tidalURLEditNote = "normalize Tidal streaming URLs: https://tickets.metabrainz.org/browse/MBBE-71"

var urlRewrites = map[*regexp.Regexp]rewriteFunc{
	// Normalize Tidal streaming URLs:
	//  https://listen.tidal.com/album/114997210 -> https://tidal.com/album/114997210
	//  https://listen.tidal.com/artist/11069    -> https://tidal.com/artist/11069
	//  https://tidal.com/browse/album/11103031  -> https://tidal.com/album/11103031
	//  https://tidal.com/browse/artist/5015356  -> https://tidal.com/artist/5015356
	//  https://tidal.com/browse/track/120087531 -> https://tidal.com/track/120087531
	regexp.MustCompile(`^https?://(?:listen\.tidal\.com|tidal\.com/browse)(/(?:album|artist|track)/\d+)$`): func(ms []string) *rewriteResult {
		return &rewriteResult{"https://tidal.com" + ms[1], tidalURLEditNote}
	},
}
