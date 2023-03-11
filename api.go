// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"golang.org/x/time/rate"
)

const (
	// https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
	apiMaxQPS    = 1
	apiUserAgent = "derat_bot/0 ( https://github.com/derat/mbbot )"
)

type api struct {
	serverURL string
	limiter   *rate.Limiter
}

func newAPI(serverURL string) *api {
	return &api{
		serverURL: serverURL,
		limiter:   rate.NewLimiter(apiMaxQPS, 1),
	}
}

// relationships contains relationship counts of different types.
type relationships struct{ artist, release, recording int }

func (a *api) getURL(ctx context.Context, mbid string) (string, *relationships, error) {
	r, err := a.send(ctx, "/ws/2/url/"+mbid+"?inc=artist-rels+release-rels+recording-rels")
	if err != nil {
		return "", nil, err
	}
	defer r.Close()

	// Parse a replace like the following (without the whitespace):
	//  <?xml version="1.0" encoding="UTF-8"?>
	//  <metadata xmlns="http://musicbrainz.org/ns/mmd-2.0#">
	//    <url id="3c395939-b7c9-43c8-b10c-c93cfa276929">
	//      <resource>https://listen.tidal.com/album/126083273</resource>
	//    </url>
	//  </metadata>
	var res struct {
		XMLName       xml.Name `xml:"metadata"`
		Resource      string   `xml:"url>resource"`
		RelationLists []struct {
			TargetType string     `xml:"target-type,attr"`
			Relations  []struct{} `xml:"relation"`
		} `xml:"url>relation-list"`
	}
	if err := xml.NewDecoder(r).Decode(&res); err != nil {
		return "", nil, err
	}

	// Count relationships of different types.
	var rels relationships
	for _, rl := range res.RelationLists {
		switch rl.TargetType {
		case "artist":
			rels.artist = len(rl.Relations)
		case "release":
			rels.release = len(rl.Relations)
		case "recording":
			rels.recording = len(rl.Relations)
		}
	}

	return res.Resource, &rels, nil
}

// notFoundErr is returned by send if a 404 error was received.
var notFoundErr = errors.New("not found")

func (a *api) send(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := a.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	u := a.serverURL + path
	log.Print("Requesting ", u)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", apiUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		if resp.StatusCode == 404 {
			return nil, notFoundErr
		}
		return nil, fmt.Errorf("server returned %v: %v", resp.StatusCode, resp.Status)
	}
	return resp.Body, nil
}
