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

// entityInfo describes an entity in the database.
type entityInfo struct {
	mbid string
	typ  entityType
	name string // or URL
	rels []relInfo
}

type entityType string

const (
	urlType entityType = "url"
)

// getEntityInfo fetches information about an entity (identified by its MBID) from srv.
func getEntityInfo(ctx context.Context, srv *server, mbid string, typ entityType) (*entityInfo, error) {
	b, err := srv.get(ctx, fmt.Sprintf("/%s/%s/edit", typ, mbid))
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
	info := entityInfo{
		mbid: ent.GID,
		typ:  entityType(ent.EntityType),
		name: ent.Name,
	}
	for _, rel := range ent.Relationships {
		info.rels = append(info.rels, rel.toRelInfo())
	}
	return &info, nil
}

// jsonData corresponds to the window.__MB__.$c object.
type jsonData struct {
	Stash struct {
		SourceEntity struct {
			GID        string `json:"gid"`
			EntityType string `json:"entityType"`
			Name       string `json:"name"`

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
