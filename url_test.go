// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestProcessURL(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(ctx, t)
	defer env.close()

	const (
		tidalMBID      = "40d2c699-f615-4f95-b212-24c344572333"
		geocitiesMBID  = "56313079-1796-4fb8-add5-d8cf117f3ba5"
		tidalStoreMBID = "545eb1f2-630f-47ff-ad38-9b15e7c0cae9"
		recmusicMBID   = "4e135691-fdc1-4127-ab69-67095aa09c44"
		doneMBID       = "e9ce6782-29e6-4f09-82b0-0abd18061e32"

		recmusicArtistMBID = "63a5c79f-697e-47e0-975d-1e2087a454aa"
	)

	env.mbidURLs[tidalMBID] = "http://listen.tidal.com/artist/11069"
	env.mbidURLs[geocitiesMBID] = "http://www.geocities.com/user"
	env.mbidURLs[tidalStoreMBID] = "https://store.tidal.com/artist/12345"
	env.mbidURLs[recmusicMBID] = "https://recmusic.jp/album/?id=1010526534"
	env.mbidURLs[doneMBID] = "https://tidal.com/album/1234" // already normalized

	env.mbidRels[geocitiesMBID] = []jsonRelationship{
		{ID: 123, LinkTypeID: 3},
		{ID: 456, LinkTypeID: 7, BeginDate: jsonDate{2000, 4, 5}},
	}
	env.mbidRels[tidalStoreMBID] = []jsonRelationship{
		{ID: 789, LinkTypeID: 85, Target: jsonTarget{EntityType: "release"}, Backward: true},
	}
	env.mbidRels[recmusicMBID] = []jsonRelationship{
		{ID: 423, LinkTypeID: 980, Target: jsonTarget{EntityType: "release", GID: recmusicArtistMBID}, Backward: true},
	}

	for _, mbid := range []string{tidalMBID, geocitiesMBID, tidalStoreMBID, recmusicMBID, doneMBID} {
		if err := processURL(ctx, env.srv, mbid, "", false); err != nil {
			t.Errorf("processURL(ctx, srv, %q, %q, false) failed: %v", mbid, "", err)
		}
	}
	want := []request{
		{
			path: "/url/" + tidalMBID + "/edit",
			params: makeURLValues(map[string]string{
				"edit-url.url":       "https://tidal.com/artist/11069",
				"edit-url.edit_note": tidalEditNote,
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    geocitiesEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "123",
				"rel-editor.rels.0.link_type":             "3",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "26",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2009",
				"rel-editor.rels.1.action":                "edit",
				"rel-editor.rels.1.id":                    "456",
				"rel-editor.rels.1.link_type":             "7",
				"rel-editor.rels.1.period.ended":          "1",
				"rel-editor.rels.1.period.end_date.day":   "26",
				"rel-editor.rels.1.period.end_date.month": "10",
				"rel-editor.rels.1.period.end_date.year":  "2009",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    tidalStoreEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "789",
				"rel-editor.rels.0.link_type":             "74",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "20",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2022",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                    recmusicEditNote,
				"rel-editor.rels.0.action":                "edit",
				"rel-editor.rels.0.id":                    "423",
				"rel-editor.rels.0.link_type":             "980",
				"rel-editor.rels.0.period.ended":          "1",
				"rel-editor.rels.0.period.end_date.day":   "1",
				"rel-editor.rels.0.period.end_date.month": "10",
				"rel-editor.rels.0.period.end_date.year":  "2021",
			}),
		},
		{
			path: "/relationship-editor",
			params: makeURLValues(map[string]string{
				"rel-editor.edit_note":                      recmusicEditNote,
				"rel-editor.rels.0.action":                  "add",
				"rel-editor.rels.0.link_type":               "980",
				"rel-editor.rels.0.period.begin_date.day":   "1",
				"rel-editor.rels.0.period.begin_date.month": "10",
				"rel-editor.rels.0.period.begin_date.year":  "2021",
				"rel-editor.rels.0.entity.0.gid":            recmusicArtistMBID,
				"rel-editor.rels.0.entity.0.type":           "release",
				"rel-editor.rels.0.entity.1.url":            "https://music.tower.jp/album/detail/1010526534",
				"rel-editor.rels.0.entity.1.type":           "url",
			}),
		},
	}
	if diff := cmp.Diff(want, env.requests, cmp.AllowUnexported(request{})); diff != "" {
		t.Error("Bad requests:\n" + diff)
	}
}

func makeURLValues(m map[string]string) url.Values {
	vals := make(url.Values)
	for k, v := range m {
		vals.Set(k, v)
	}
	return vals
}

func TestRunURLFunc(t *testing.T) {
	d := date{2003, 7, 9} // arbitrary

	for _, tc := range []struct {
		url         string
		rels        []relInfo
		rewritten   string // rewritten URL; "" means no rewrite
		updatedRels []relInfo
		newURLs     []entityInfo
	}{
		{"https://www.example.org/", nil, "", nil, nil},
		{"https://www.example.org/artist/123", nil, "", nil, nil},
		{"https://www.example.org/", []relInfo{{targetType: "release"}}, "", nil, nil},

		// Tidal (MBBE-71)
		{"https://tidal.com/album/11069", nil, "", nil, nil},      // already canonicalized
		{"https://test.tidal.com/album/11069", nil, "", nil, nil}, // unknown hostname
		{"http://www.tidal.com/test/11069", nil, "", nil, nil},    // unknown path component
		{"http://tidal.com/album/11069", nil, "https://tidal.com/album/11069", nil, nil},
		{"https://listen.tidal.com/artist/11069", nil, "https://tidal.com/artist/11069", nil, nil},
		{"https://tidal.com/browse/track/11069", nil, "https://tidal.com/track/11069", nil, nil},
		{"https://www.tidal.com/album/11069", nil, "https://tidal.com/album/11069", nil, nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "release"}},
			"https://tidal.com/album/123", nil, nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "recording"}},
			"https://tidal.com/track/456", nil, nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "release"}, {targetType: "recording"}},
			"https://tidal.com/track/456", nil, nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "artist"}}, "", nil, nil},
		{"https://desktop.tidal.com/album/163812859", nil, "https://tidal.com/album/163812859", nil, nil},
		{"http://tidal.com/browse/album/119425271?play=true", nil, "https://tidal.com/album/119425271", nil, nil},
		{"https://tidal.com/browse/album/126495793/", nil, "https://tidal.com/album/126495793", nil, nil},
		{"https://listen.tidal.com/video/78581329", nil, "https://tidal.com/video/78581329", nil, nil},
		{"https://www.tidal.com/browse/track/155221653", nil, "https://tidal.com/track/155221653", nil, nil},

		// GeoCities (MBBE-47)
		{"http://www.geocities.com/test/", nil, "", nil, nil}, // no relationships
		{"http://www.geocities.com/test/", []relInfo{{targetType: "artist", ended: true, endDate: d}},
			"", nil, nil}, // already ended
		{"http://www.geocities.com/test/", []relInfo{{targetType: "artist", beginDate: d}},
			"", []relInfo{{targetType: "artist", beginDate: d, ended: true, endDate: geocitiesEndDate}}, nil},
		{"http://geocities.yahoo.co.jp/test/", []relInfo{{targetType: "artist", beginDate: d}, {targetType: "release", beginDate: d}},
			"", []relInfo{
				{targetType: "artist", beginDate: d, ended: true, endDate: geocitiesJapanEndDate},
				{targetType: "release", beginDate: d, ended: true, endDate: geocitiesJapanEndDate},
			}, nil},

		// Tidal Store (MBBE-63)
		{"https://store.tidal.com/artist/123", nil, "", nil, nil}, // no relationships
		{"https://store.tidal.com/artist/123", []relInfo{{targetType: "artist", linkTypeID: 176, ended: true, endDate: d}},
			"", nil, nil}, // already ended
		{"https://store.tidal.com/artist/123", []relInfo{{targetType: "artist", linkTypeID: 176}},
			"", []relInfo{{targetType: "artist", linkTypeID: 176, ended: true, endDate: tidalStoreEndDate}}, nil},
		{"https://store.tidal.com/artist/123", []relInfo{{targetType: "artist", linkTypeID: 194, ended: true, endDate: d}},
			"", []relInfo{{targetType: "artist", linkTypeID: 176, ended: true, endDate: d}}, nil},
		{"https://tidal.com/store/album/123", []relInfo{{targetType: "release", linkTypeID: 85}},
			"", []relInfo{{targetType: "release", linkTypeID: 74, ended: true, endDate: tidalStoreEndDate}}, nil},
		{"https://tidal.com/us/store/track/123", []relInfo{{targetType: "recording", linkTypeID: 268}},
			"", []relInfo{{targetType: "recording", linkTypeID: 254, ended: true, endDate: tidalStoreEndDate}}, nil},

		// RecMusic (MBBE-48) and Tower Records Music (MBBE-49)
		{"https://recmusic.jp/album/?id=1010526534", nil, "", nil, nil}, // no relationships
		{"https://recmusic.jp/album/?id=1010526534", []relInfo{{targetType: "release", linkTypeID: 980, ended: true, endDate: d}},
			"", nil, []entityInfo{{
				typ:  urlType,
				name: "https://music.tower.jp/album/detail/1010526534",
				rels: []relInfo{{targetType: "release", linkTypeID: 980, beginDate: recmusicEndDate}},
			}}}, // already ended
		{"https://recmusic.jp/artist/?id=2000017248", []relInfo{{targetType: "artist", linkTypeID: 978}},
			"", []relInfo{{targetType: "artist", linkTypeID: 978, endDate: recmusicEndDate, ended: true}},
			[]entityInfo{{
				typ:  urlType,
				name: "https://music.tower.jp/artist/detail/2000017248",
				rels: []relInfo{{targetType: "artist", linkTypeID: 978, beginDate: recmusicEndDate}},
			}}},
		{"https://recmusic.jp/artist/?id=2001445271", []relInfo{{targetType: "artist", linkTypeID: 978}},
			"", []relInfo{{targetType: "artist", linkTypeID: 978, endDate: recmusicEndDate, ended: true}}, nil}, // no longer active
	} {
		if tc.rewritten == "" {
			tc.rewritten = tc.url
		}
		orig := entityInfo{name: tc.url, rels: tc.rels, typ: urlType}
		res := runURLFunc(&orig)
		if res == nil {
			if tc.rewritten != tc.url || len(tc.updatedRels) > 0 || len(tc.newURLs) > 0 {
				t.Errorf("runURLFunc(%v) didn't rewrite; want %q, %v, %v", orig, tc.rewritten, tc.updatedRels, tc.newURLs)
			}
			continue
		}

		if res.rewritten != tc.rewritten {
			t.Errorf("runURLFunc(%v) rewrote URL to %q; want %q", orig, res.rewritten, tc.rewritten)
		}
		if !reflect.DeepEqual(res.updatedRels, tc.updatedRels) {
			t.Errorf("runURLFunc(%v) updated rels %v; want %v", orig, res.updatedRels, tc.updatedRels)
		}
		if !reflect.DeepEqual(res.newURLs, tc.newURLs) {
			t.Errorf("runURLFunc(%v) added URLs %v; want %v", orig, res.newURLs, tc.newURLs)
		}
	}
}
