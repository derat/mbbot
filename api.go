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

// urlInfo describes a URL in the database.
type urlInfo struct {
	url  string
	rels relInfos
}

// relInfo describes a relationship between one entity and another.
type relInfo struct {
	typeDesc   string
	typeMBID   string
	targetMBID string
	backward   bool
	endDate    string
	ended      bool
}

// relInfos holds information about relationships of different types.
type relInfos struct{ artistRels, releaseRels, recordingRels []relInfo }

func (a *api) getURL(ctx context.Context, mbid string) (*urlInfo, error) {
	r, err := a.send(ctx, "/ws/2/url/"+mbid+"?inc=artist-rels+release-rels+recording-rels")
	if err != nil {
		return nil, err
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
		Resource      string   `xml:"url>resource"` // e.g. "http://www.geocities.jp/orgel0104/"
		RelationLists []struct {
			TargetType string `xml:"target-type,attr"` // e.g. "artist"
			Relations  []struct {
				Type      string `xml:"type,attr"`    // e.g. "official homepage"
				TypeID    string `xml:"type-id,attr"` // e.g. "fe33d22f-c3b0-4d68-bd53-a856badf2b15"
				Target    string `xml:"target"`       // e.g. "ef673d88-4c3c-4c90-a46d-2ee30946b6f0"
				Direction string `xml:"direction"`    // e.g. "backward"
				End       string `xml:"end"`          // e.g. "2019-03-31"
				Ended     bool   `xml:"ended"`
			} `xml:"relation"`
		} `xml:"url>relation-list"`
	}
	if err := xml.NewDecoder(r).Decode(&res); err != nil {
		return nil, err
	}

	info := urlInfo{url: res.Resource}

	for _, rl := range res.RelationLists {
		var dst *[]relInfo
		switch rl.TargetType {
		case "artist":
			dst = &info.rels.artistRels
		case "release":
			dst = &info.rels.releaseRels
		case "recording":
			dst = &info.rels.recordingRels
		}
		if dst == nil {
			continue
		}
		for _, rel := range rl.Relations {
			*dst = append(*dst, relInfo{
				typeDesc:   rel.Type,
				typeMBID:   rel.TypeID,
				targetMBID: rel.Target,
				backward:   rel.Direction == "backward",
				endDate:    rel.End,
				ended:      rel.Ended,
			})
		}
	}

	return &info, nil
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
