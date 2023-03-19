// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// urlInfo describes a URL in the database.
// TODO: Rename this to entityInfo and make it type-agnostic.
type urlInfo struct {
	url  string
	rels []relInfo
}

// getURLInfo fetches information about a URL (identified by its MBID) from srv.
// TODO: Consider updating this to get info about arbitrary entity types.
// Probably the only parts that need to change are the URL path and Decoded field.
func getURLInfo(ctx context.Context, srv *server, mbid string) (*urlInfo, error) {
	b, err := srv.get(ctx, "/url/"+mbid+"/edit")
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
