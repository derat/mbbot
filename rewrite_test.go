// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"reflect"
	"testing"
)

func TestDoRewrite(t *testing.T) {
	d := date{2003, 7, 9} // arbitrary

	for _, tc := range []struct {
		url         string
		rels        []relInfo
		rewritten   string // rewritten URL; "" means no rewrite
		updatedRels []relInfo
	}{
		{"https://www.example.org/", nil, "", nil},
		{"https://www.example.org/artist/123", nil, "", nil},
		{"https://www.example.org/", []relInfo{{targetType: "release"}}, "", nil},

		// Tidal (MBBE-71)
		{"https://tidal.com/album/11069", nil, "", nil},      // already canonicalized
		{"https://test.tidal.com/album/11069", nil, "", nil}, // unknown hostname
		{"http://www.tidal.com/test/11069", nil, "", nil},    // unknown path component
		{"http://tidal.com/album/11069", nil, "https://tidal.com/album/11069", nil},
		{"https://listen.tidal.com/artist/11069", nil, "https://tidal.com/artist/11069", nil},
		{"https://tidal.com/browse/track/11069", nil, "https://tidal.com/track/11069", nil},
		{"https://www.tidal.com/album/11069", nil, "https://tidal.com/album/11069", nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "release"}},
			"https://tidal.com/album/123", nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "recording"}},
			"https://tidal.com/track/456", nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "release"}, {targetType: "recording"}},
			"https://tidal.com/track/456", nil},
		{"https://listen.tidal.com/album/123/track/456", []relInfo{{targetType: "artist"}}, "", nil},
		{"https://desktop.tidal.com/album/163812859", nil, "https://tidal.com/album/163812859", nil},
		{"http://tidal.com/browse/album/119425271?play=true", nil, "https://tidal.com/album/119425271", nil},
		{"https://tidal.com/browse/album/126495793/", nil, "https://tidal.com/album/126495793", nil},
		{"https://listen.tidal.com/video/78581329", nil, "https://tidal.com/video/78581329", nil},
		{"https://www.tidal.com/browse/track/155221653", nil, "https://tidal.com/track/155221653", nil},

		// GeoCities (MBBE-47)
		{"http://www.geocities.com/test/", nil, "", nil},                                                        // no relationships
		{"http://www.geocities.com/test/", []relInfo{{targetType: "artist", ended: true, endDate: d}}, "", nil}, // already ended
		{"http://www.geocities.com/test/", []relInfo{{targetType: "artist", beginDate: d}},
			"", []relInfo{{targetType: "artist", beginDate: d, ended: true, endDate: geocitiesEndDate}}},
		{"http://geocities.yahoo.co.jp/test/", []relInfo{{targetType: "artist", beginDate: d}, {targetType: "release", beginDate: d}},
			"", []relInfo{
				{targetType: "artist", beginDate: d, ended: true, endDate: geocitiesJapanEndDate},
				{targetType: "release", beginDate: d, ended: true, endDate: geocitiesJapanEndDate},
			}},
	} {
		if tc.rewritten == "" {
			tc.rewritten = tc.url
		}
		if res := doRewrite(urlRewrites, tc.url, tc.rels); res == nil {
			if tc.rewritten != tc.url || len(tc.updatedRels) > 0 {
				t.Errorf("doRewrite(urlRewrites, %q, %v) didn't rewrite; want %q, %v",
					tc.url, tc.rels, tc.rewritten, tc.updatedRels)
			}
		} else {
			if res.rewritten != tc.rewritten {
				t.Errorf("doRewrite(urlRewrites, %q, %v) rewrote URL to %q; want %q",
					tc.url, tc.rels, res.rewritten, tc.rewritten)
			}
			if !reflect.DeepEqual(res.updatedRels, tc.updatedRels) {
				t.Errorf("doRewrite(urlRewrites, %q, %v) updated rels %v; want %v",
					tc.url, tc.rels, res.updatedRels, tc.updatedRels)
			}
		}
	}
}
