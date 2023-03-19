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
	makeVotable := flag.Bool("make-votable", false, "Force voting on edits")
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
	ed, err := newEditor(ctx, *server, user, pass, editorDryRun(*dryRun))
	if err != nil {
		log.Fatal("Failed logging in: ", err)
	}

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
			} else if err := updateURL(ctx, ed, mbid, *editNote, *makeVotable); err != nil {
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
// If makeVotable is true, voting will be forced.
// If no updates are performed, a nil error is returned.
func updateURL(ctx context.Context, ed *editor, mbid, editNote string,
	makeVotable bool) error {
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
		vals := map[string]string{
			"edit-url.url":       res.rewritten,
			"edit-url.edit_note": res.editNote,
		}
		if makeVotable {
			vals["edit-url.make_votable"] = "1"
		}
		b, err := ed.post(ctx, "/url/"+mbid+"/edit", vals)
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
		vals := make(map[string]string)
		for i, rel := range res.updatedRels {
			log.Printf("%v: editing relationship %v (%q)", mbid, rel.id, rel.desc(info.url))
			pre := fmt.Sprintf("rel-editor.rels.%d.", i)
			if err := setRelEditVals(vals, pre, rel, oldRels[rel.id]); err != nil {
				return err
			}
		}
		if ids, err := postRelEdit(ctx, ed, vals, res.editNote, makeVotable); err != nil {
			return err
		} else {
			log.Printf("%v: edited %v relationship(s)", mbid, len(ids))
		}
	}

	for _, info := range res.newURLs {
		vals := make(map[string]string)
		for i, rel := range info.rels {
			log.Printf("%v: adding relationship (%q)", mbid, rel.desc(info.url))
			pre := fmt.Sprintf("rel-editor.rels.%d.", i)
			if err := setRelEditVals(vals, pre, rel, nil); err != nil {
				return err
			}
			// I think that the "normal" ordering sorts entities by type name, so we should use
			// [artist,url], [recording,url], and [release,url], but [url,work]. So weird.
			if (rel.backward && rel.targetType > "url") || (!rel.backward && rel.targetType < "url") {
				return fmt.Errorf("incorrect direction for relationship %q", rel.desc(info.url))
			}
			urlPre, targetPre := pre+"entity.0", pre+"entity.1"
			if rel.backward {
				urlPre, targetPre = targetPre, urlPre
			}
			vals[urlPre+".url"] = info.url
			vals[urlPre+".type"] = "url"
			vals[targetPre+".gid"] = rel.targetMBID
			vals[targetPre+".type"] = rel.targetType
		}
		if ids, err := postRelEdit(ctx, ed, vals, res.editNote, makeVotable); err != nil {
			return err
		} else {
			for _, id := range ids {
				log.Printf("%v: added relationship %v", mbid, id)
			}
		}
	}

	return nil
}

// setRelEditVals sets values needed by the /relationship-editor endpoint.
// pre is prepended to each parameter name and should be e.g. "rel-editor.rels.0".
// If orig is non-nil, an "edit" request is set with differences between orig and rel.
// If orig is nil, an "add" request is set to create a new relationship.
// The caller must set entity-related parameters when creating new relationships.
func setRelEditVals(vals map[string]string, pre string, rel relInfo, orig *relInfo) error {
	if orig == nil {
		if rel.id != 0 {
			return fmt.Errorf("invalid rel %d", rel.id)
		}
	} else if rel == *orig {
		return fmt.Errorf("no changes for rel %d", rel.id)
	}

	// These parameters are handled by lib/MusicBrainz/Server/Controller/RelationshipEditor.pm.
	origCnt := len(vals)
	linkTypeKey := pre + "link_type"
	if orig == nil || rel.linkTypeID != orig.linkTypeID {
		vals[linkTypeKey] = strconv.Itoa(rel.linkTypeID)
	}
	// TODO: Support clearing dates too?
	if !rel.beginDate.empty() && (orig == nil || rel.beginDate != orig.beginDate) {
		vals[pre+"period.begin_date.year"] = strconv.Itoa(rel.beginDate.year)
		vals[pre+"period.begin_date.month"] = strconv.Itoa(rel.beginDate.month)
		vals[pre+"period.begin_date.day"] = strconv.Itoa(rel.beginDate.day)
	}
	if !rel.endDate.empty() && (orig == nil || rel.endDate != orig.endDate) {
		vals[pre+"period.end_date.year"] = strconv.Itoa(rel.endDate.year)
		vals[pre+"period.end_date.month"] = strconv.Itoa(rel.endDate.month)
		vals[pre+"period.end_date.day"] = strconv.Itoa(rel.endDate.day)
	}
	if (orig == nil && rel.ended) || (orig != nil && rel.ended != orig.ended) {
		vals[pre+"period.ended"] = boolToParam(rel.ended)
	}
	if len(vals) == origCnt {
		return fmt.Errorf("unsupported update for rel (%+v)", rel)
	}

	// The server returns a 400 error if "action" and "link_type" are omitted.
	if orig == nil {
		vals[pre+"action"] = "add"
	} else {
		vals[pre+"action"] = "edit"
		vals[pre+"id"] = strconv.Itoa(rel.id)
		if _, ok := vals[linkTypeKey]; !ok {
			vals[linkTypeKey] = strconv.Itoa(rel.linkTypeID)
		}
	}

	return nil
}

// postRelEdit posts vals to /relationship-editor.
// IDs of created relationships are returned.
// If existing relationships are edited, IDs are 0.
func postRelEdit(ctx context.Context, ed *editor, vals map[string]string,
	editNote string, makeVotable bool) ([]int, error) {
	// Set additional parameters.
	vals["rel-editor.edit_note"] = editNote
	if makeVotable {
		vals["rel-editor.make_votable"] = "1"
	}

	b, err := ed.post(ctx, "/relationship-editor", vals)
	if err != nil {
		return nil, fmt.Errorf("%v (%q)", err, b)
	}
	// The response is written by submit_edits in lib/MusicBrainz/Server/Controller/WS/js/Edit.pm,
	// which oddly doesn't include the actual edit IDs.
	var data struct {
		Edits []struct {
			RelationshipID int `json:"relationship_id"`
			EditType       int `json:"edit_type"`
			Response       int `json:"response"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %v", err)
	}
	var ids []int
	for i, ed := range data.Edits {
		if ed.Response != 1 {
			return ids, fmt.Errorf("relationship edit %v with type %v failed: %v", i, ed.EditType, ed.Response)
		}
		ids = append(ids, ed.RelationshipID)
	}
	return ids, nil
}

// boolToParam returns a string corresponding to v to use as a parameter passed to MusicBrainz.
// boolean_from_json() in lib/MusicBrainz/Server/Data/Utils.pm seems to regrettably use Perl truthiness.
func boolToParam(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
