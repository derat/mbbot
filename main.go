// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
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
	srv, err := newServer(ctx, *server, user, pass, serverDryRun(*dryRun))
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
			} else if err := cancelEdit(ctx, srv, id, *editNote); err != nil {
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
			} else if err := processURL(ctx, srv, mbid, *editNote, *makeVotable); err != nil {
				log.Printf("Failed processing %v: %v", mbid, err)
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
func cancelEdit(ctx context.Context, srv *server, id int, editNote string) error {
	log.Printf("Canceling edit %d", id)
	_, err := srv.post(ctx, fmt.Sprintf("/edit/%d/cancel", id), map[string]string{
		"confirm.edit_note": editNote,
	})
	return err
}
