// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"fmt"
	"regexp"
)

// TODO: Rename this file to url.go and make everything URL-specific.
// I don't think this code will end up being useful for rewriting anything else.

// doRewrite looks for an appropriate rewrite for orig.
// If orig isn't matched by a rewrite or is unchanged after rewriting, nil is returned.
func doRewrite(rm rewriteMap, orig string, rels []relInfo) *rewriteResult {
	for re, fn := range rm {
		if ms := re.FindStringSubmatch(orig); ms != nil {
			res := fn(ms, rels)
			if res == nil || (res.rewritten == orig && len(res.updatedRels) == 0 && len(res.newURLs) == 0) {
				return nil // unchanged
			}
			return res
		}
	}
	return nil
}

// rewriteFunc accepts the match groups returned by FindStringSubmatch and returns
// a rewritten string and updated relationships. nil may be returned to abort the rewrite.
type rewriteFunc func(ms []string, rels []relInfo) *rewriteResult

type rewriteResult struct {
	rewritten   string    // rewritten original string
	updatedRels []relInfo // relationships to update (others left unchanged)
	newURLs     []urlInfo
	editNote    string // https://musicbrainz.org/doc/Edit_Note
}

type rewriteMap map[*regexp.Regexp]rewriteFunc

const (
	tidalEditNote      = "normalize Tidal streaming URLs: https://tickets.metabrainz.org/browse/MBBE-71"
	geocitiesEditNote  = "end GeoCities relationships: https://tickets.metabrainz.org/browse/MBBE-47"
	tidalStoreEditNote = "end Tidal Store relationships: https://tickets.metabrainz.org/browse/MBBE-63"
	recmusicEditNote   = "convert RecMusic URLs to Tower Records Music: " +
		"https://tickets.metabrainz.org/browse/MBBE-48, " +
		"https://tickets.metabrainz.org/browse/MBBE-49"
)

var (
	geocitiesEndDate      = date{2009, 10, 26} // see https://en.wikipedia.org/wiki/Yahoo!_GeoCities
	geocitiesJapanEndDate = date{2019, 3, 31}
	tidalStoreEndDate     = date{2022, 10, 20}
	recmusicEndDate       = date{2021, 10, 1} // also music.tower.jp start date
)

var tidalAlbumTrackRegexp = regexp.MustCompile(`^/album/(\d+)/track/(\d+)$`)

// missingTowerRecordsPairs contains [type, id] pairs for recmusic.jp URLs that don't work after
// rewriting to music.tower.jp.
var missingTowerRecordsPairs = map[[2]string]struct{}{
	{"artist", "2001445271"}: struct{}{},
	{"album", "1016070930"}:  struct{}{},
}

var urlRewrites = rewriteMap{
	// MBBE-71: Normalize Tidal streaming URLs:
	//  https://listen.tidal.com/album/114997210 -> https://tidal.com/album/114997210
	//  https://listen.tidal.com/artist/11069    -> https://tidal.com/artist/11069
	//  https://tidal.com/browse/album/11103031  -> https://tidal.com/album/11103031
	//  https://tidal.com/browse/artist/5015356  -> https://tidal.com/artist/5015356
	//  https://tidal.com/browse/track/120087531 -> https://tidal.com/track/120087531
	//  (and many other forms; see TestDoRewrite_URL)
	regexp.MustCompile(`^https?://` + // both http:// and https://
		`(?:(?:desktop\.|desktop\.stage\.|listen\.|www\.)?tidal\.com)` + // hostname
		`(?:/browse)?` + // optional /browse component
		`(/(?:album|artist|track|video|album/\d+/track)/\d+)` + // match significant components, e.g. /album/123
		`(?:/|\?.*)?` + // trailing slash or query
		`$`): func(ms []string, rels []relInfo) *rewriteResult {
		p := ms[1]
		res := rewriteResult{
			rewritten: "https://tidal.com" + p,
			editNote:  tidalEditNote,
		}

		// If the URL contains both an album and a track, use its relationships to
		// figure out what it should actually be.
		if ms := tidalAlbumTrackRegexp.FindStringSubmatch(p); ms != nil {
			album, track := ms[1], ms[2]
			if len(filterRels(rels, "recording")) > 0 {
				res.rewritten = "https://tidal.com/track/" + track
			} else if len(filterRels(rels, "release")) > 0 {
				res.rewritten = "https://tidal.com/album/" + album
			} else {
				return nil // give up if it's related to neither
			}
		}

		return &res
	},

	// MBBE-47: Mark GeoCities URL relationships as ended.
	regexp.MustCompile(`^https?://` + // both http:// and https://
		`(?:[-a-z0-9]+\.)?geocities\.(?:yahoo\.)?(com|jp|co\.jp)` + // hostname (capture TLD)
		`/.*` + // all paths
		`$`): func(ms []string, rels []relInfo) *rewriteResult {
		res := rewriteResult{
			rewritten: ms[0], // leave the URL alone
			editNote:  geocitiesEditNote,
		}
		endDate := geocitiesEndDate
		if ms[1] == "jp" || ms[1] == "co.jp" {
			endDate = geocitiesJapanEndDate
		}
		for _, rel := range rels {
			if !rel.ended {
				rel.ended = true
				rel.endDate = endDate
				res.updatedRels = append(res.updatedRels, rel)
			}
		}
		if len(res.updatedRels) == 0 {
			return nil
		}
		return &res
	},

	// MBBE-63: Mark Tidal Store URL relationships as ended.
	regexp.MustCompile(`^https?://` +
		`(store\.tidal\.com|tidal\.com(/[a-zA-Z]{2})?/store)` +
		`/.*` +
		`$`): func(ms []string, rels []relInfo) *rewriteResult {
		res := rewriteResult{
			rewritten: ms[0], // leave the URL alone
			editNote:  tidalStoreEditNote,
		}
		// TODO: The server returns an error if an edit would create a duplicate relationship
		// (i.e. same link type, source, and target). This code could try to handle that case, but
		// I've instead manually created edits to clean up the few URLs with multiple relationships.
		for _, rel := range rels {
			orig := rel
			switch rel.targetType {
			case "artist":
				rel.linkTypeID = 176 // "music can be purchased for download at"
			case "release":
				rel.linkTypeID = 74 // "can be purchased for download at"
			case "recording":
				rel.linkTypeID = 254 // "can be purchased for download at"
			}
			if !rel.ended {
				rel.ended = true
				rel.endDate = tidalStoreEndDate
			}
			if rel != orig {
				res.updatedRels = append(res.updatedRels, rel)
			}
		}
		if len(res.updatedRels) == 0 {
			return nil
		}
		return &res
	},

	// MBBE-48: Mark RecMusic links as ended
	// MBBE-49: Migrate RecMusic URLs to Tower Records Music URLs
	regexp.MustCompile(`^https?://` +
		`recmusic\.jp/(?:[a-z][a-z]/)?` + // hostname plus optional country code ("sp/")
		`(artist|album)/\?id=(\d+)` + // capture entity type and numeric ID
		`$`): func(ms []string, rels []relInfo) *rewriteResult {
		if len(rels) == 0 {
			return nil
		}

		res := rewriteResult{
			rewritten: ms[0], // leave the URL alone
			editNote:  recmusicEditNote,
		}

		newURL := urlInfo{url: fmt.Sprintf("https://music.tower.jp/%s/detail/%s", ms[1], ms[2])}
		for _, rel := range rels {
			orig := rel
			if !rel.ended {
				rel.ended = true
				rel.endDate = recmusicEndDate
			}
			if rel != orig {
				res.updatedRels = append(res.updatedRels, rel)
			}

			if _, ok := missingTowerRecordsPairs[[2]string{ms[1], ms[2]}]; !ok {
				newRel := orig
				newRel.id = 0
				newRel.beginDate = recmusicEndDate
				newRel.endDate = date{}
				newRel.ended = false
				newURL.rels = append(newURL.rels, newRel)
			}
		}
		if len(newURL.rels) > 0 {
			res.newURLs = append(res.newURLs, newURL)
		}
		return &res
	},
}
