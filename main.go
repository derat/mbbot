// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	actionCancel = "cancel" // cancel edits with IDs read from stdin
	actionURLs   = "urls"   // update URLs corresponding to MBIDs read from stdin
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

	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Positional args are not accepted")
		os.Exit(2)
	}

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
			} else if err := updateURL(ctx, ed, mbid, *editNote); err != nil {
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

// updateURL attempts to update the URL with the specified MBID.
// If editNote is non-empty, it will be attached to the edit.
// If no updates are performed, a nil error is returned.
func updateURL(ctx context.Context, ed *editor, mbid, editNote string) error {
	info, err := ed.getURLInfo(ctx, mbid)
	if err != nil {
		return fmt.Errorf("failed getting URL: %v", err)
	}
	res := doRewrite(urlRewrites, info.url, info.rels)
	if res == nil {
		log.Printf("%v: no rewrites found for %v", mbid, info.url)
		return nil
	}
	if editNote != "" {
		res.editNote = editNote
	}

	if res.rewritten != "" && res.rewritten != info.url {
		log.Printf("%v: rewriting %v to %v", mbid, info.url, res.rewritten)
		b, err := ed.post(ctx, "/url/"+mbid+"/edit", map[string]string{
			"edit-url.url":       res.rewritten,
			"edit-url.edit_note": res.editNote,
		})
		if err != nil {
			return err
		}
		ms := ed.editIDRegexp.FindStringSubmatch(string(b))
		if ms == nil {
			return errors.New("didn't find edit ID")
		}
		log.Printf("%v: created edit #%s", mbid, ms[1])
	}

	if len(res.updatedRels) > 0 {
		oldRels := make(map[int]*relInfo, len(info.rels))
		for i := range info.rels {
			oldRels[info.rels[i].id] = &info.rels[i]
		}
		vals := map[string]string{"rel-editor.edit_note": res.editNote}
		for i, rel := range res.updatedRels {
			log.Printf("%v: updating relationship %v (%q)", mbid, rel.id, rel.desc(info.url))
			pre := fmt.Sprintf("rel-editor.rels.%d.", i)
			if err := setRelEditVals(vals, pre, oldRels[rel.id], &rel); err != nil {
				return err
			}
		}
		b, err := ed.post(ctx, "/relationship-editor", vals)
		if err != nil {
			return err
		}
		// This is written by submit_edits in lib/MusicBrainz/Server/Controller/WS/js/Edit.pm,
		// which oddly doesn't include edit IDs.
		var data struct {
			Edits []struct {
				EditType int `json:"edit_type"`
				Response int `json:"response"`
			} `json:"edits"`
		}
		if err := json.Unmarshal(b, &data); err != nil {
			return fmt.Errorf("unmarshaling response: %v", err)
		}
		for i, ed := range data.Edits {
			if ed.Response != 1 {
				return fmt.Errorf("relationship edit %v with type %v failed: %v", i, ed.EditType, ed.Response)
			}
		}
		log.Printf("%v: created %v relationship edit(s)", mbid, len(data.Edits))
	}

	return nil
}

func setRelEditVals(vals map[string]string, pre string, orig, updated *relInfo) error {
	if orig == nil {
		return fmt.Errorf("invalid rel %d", updated.id)
	} else if *orig == *updated {
		return fmt.Errorf("no changes for rel %d", updated.id)
	}

	origCnt := len(vals)
	if updated.ended != orig.ended {
		vals[pre+"period.ended"] = fmt.Sprint(updated.ended) // "true" or "false"
	}
	if updated.endDate != orig.endDate {
		vals[pre+"period.end_date.year"] = strconv.Itoa(updated.endDate.year)
		vals[pre+"period.end_date.month"] = strconv.Itoa(updated.endDate.month)
		vals[pre+"period.end_date.day"] = strconv.Itoa(updated.endDate.day)
	}
	if len(vals) == origCnt {
		return fmt.Errorf("unsupported update for rel (%+v -> %+v)", *orig, *updated)
	}

	// The server returns a 400 error if "action" and "link_type" are omitted.
	vals[pre+"action"] = "edit"
	vals[pre+"id"] = strconv.Itoa(updated.id)
	vals[pre+"link_type"] = strconv.Itoa(updated.linkTypeID)

	return nil
}
