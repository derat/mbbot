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
		newURLs     []urlInfo
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
			"", nil, []urlInfo{{"https://music.tower.jp/album/detail/1010526534", []relInfo{
				{targetType: "release", linkTypeID: 980, beginDate: recmusicEndDate},
			}}}}, // already ended
		{"https://recmusic.jp/artist/?id=2000017248", []relInfo{{targetType: "artist", linkTypeID: 978}},
			"", []relInfo{{targetType: "artist", linkTypeID: 978, endDate: recmusicEndDate, ended: true}},
			[]urlInfo{{"https://music.tower.jp/artist/detail/2000017248", []relInfo{
				{targetType: "artist", linkTypeID: 978, beginDate: recmusicEndDate},
			}}}},
		{"https://recmusic.jp/artist/?id=2001445271", []relInfo{{targetType: "artist", linkTypeID: 978}},
			"", []relInfo{{targetType: "artist", linkTypeID: 978, endDate: recmusicEndDate, ended: true}}, nil}, // no longer active
	} {
		if tc.rewritten == "" {
			tc.rewritten = tc.url
		}
		res := doRewrite(urlRewrites, tc.url, tc.rels)
		if res == nil {
			if tc.rewritten != tc.url || len(tc.updatedRels) > 0 || len(tc.newURLs) > 0 {
				t.Errorf("doRewrite(urlRewrites, %q, %v) didn't rewrite; want %q, %v, %v",
					tc.url, tc.rels, tc.rewritten, tc.updatedRels, tc.newURLs)
			}
			continue
		}

		if res.rewritten != tc.rewritten {
			t.Errorf("doRewrite(urlRewrites, %q, %v) rewrote URL to %q; want %q",
				tc.url, tc.rels, res.rewritten, tc.rewritten)
		}
		if !reflect.DeepEqual(res.updatedRels, tc.updatedRels) {
			t.Errorf("doRewrite(urlRewrites, %q, %v) updated rels %v; want %v",
				tc.url, tc.rels, res.updatedRels, tc.updatedRels)
		}
		if !reflect.DeepEqual(res.newURLs, tc.newURLs) {
			t.Errorf("doRewrite(urlRewrites, %q, %v) added URLs %v; want %v",
				tc.url, tc.rels, res.newURLs, tc.newURLs)
		}
	}
}
