// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
)

// processURL attempts to process the URL with the specified MBID.
// If editNote is non-empty, it will be attached to the edit.
// If makeVotable is true, voting will be forced.
// If no updates are performed, a nil error is returned.
func processURL(ctx context.Context, srv *server, mbid, editNote string, makeVotable bool) error {
	info, err := getEntityInfo(ctx, srv, mbid, urlType)
	if err != nil {
		return fmt.Errorf("failed getting URL: %v", err)
	}
	res := runURLFunc(info)
	if res == nil {
		log.Printf("%v: no rewrites found for %v", mbid, info.name)
		return nil
	}
	if editNote != "" {
		res.editNote = editNote
	}

	if res.rewritten != "" && res.rewritten != info.name {
		log.Printf("%v: rewriting %v to %v", mbid, info.name, res.rewritten)
		vals := map[string]string{
			"edit-url.url":       res.rewritten,
			"edit-url.edit_note": res.editNote,
		}
		if makeVotable {
			vals["edit-url.make_votable"] = "1"
		}
		b, err := srv.post(ctx, "/url/"+mbid+"/edit", vals)
		if err != nil {
			return err
		}
		ms := srv.editIDRegexp.FindStringSubmatch(string(b))
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
			log.Printf("%v: editing relationship %v (%q)", mbid, rel.id, rel.desc(info.name))
			pre := fmt.Sprintf("rel-editor.rels.%d.", i)
			if err := setRelEditVals(vals, pre, rel, oldRels[rel.id]); err != nil {
				return err
			}
		}
		if ids, err := postRelEdit(ctx, srv, vals, res.editNote, makeVotable); err != nil {
			return err
		} else {
			log.Printf("%v: edited %v relationship(s)", mbid, len(ids))
		}
	}

	for _, info := range res.newURLs {
		vals := make(map[string]string)
		for i, rel := range info.rels {
			log.Printf("%v: adding relationship (%q)", mbid, rel.desc(info.name))
			pre := fmt.Sprintf("rel-editor.rels.%d.", i)
			if err := setRelEditVals(vals, pre, rel, nil); err != nil {
				return err
			}
			// I think that the "normal" ordering sorts entities by type name, so we should use
			// [artist,url], [recording,url], and [release,url], but [url,work]. So weird.
			if (rel.backward && rel.targetType > "url") || (!rel.backward && rel.targetType < "url") {
				return fmt.Errorf("incorrect direction for relationship %q", rel.desc(info.name))
			}
			urlPre, targetPre := pre+"entity.0", pre+"entity.1"
			if rel.backward {
				urlPre, targetPre = targetPre, urlPre
			}
			vals[urlPre+".url"] = info.name
			vals[urlPre+".type"] = "url"
			vals[targetPre+".gid"] = rel.targetMBID
			vals[targetPre+".type"] = rel.targetType
		}
		if ids, err := postRelEdit(ctx, srv, vals, res.editNote, makeVotable); err != nil {
			return err
		} else {
			for _, id := range ids {
				log.Printf("%v: added relationship %v", mbid, id)
			}
		}
	}

	return nil
}

// runURLFunc looks for an appropriate function in urlFuncs for the supplied URL.
// If the URL isn't matched or is unchanged after processing, nil is returned.
func runURLFunc(url *entityInfo) *urlResult {
	for re, fn := range urlFuncs {
		if ms := re.FindStringSubmatch(url.name); ms != nil {
			cp := *url
			cp.rels = append([]relInfo(nil), url.rels...)
			res := fn(&cp, ms)
			if res == nil || (res.rewritten == url.name && len(res.updatedRels) == 0 && len(res.newURLs) == 0) {
				return nil // unchanged
			}
			return res
		}
	}
	return nil
}

// urlFunc accepts the match groups returned by FindStringSubmatch and returns updates.
// nil may be returned to abort processing.
type urlFunc func(url *entityInfo, ms []string) *urlResult

type urlResult struct {
	rewritten   string    // rewritten URL
	updatedRels []relInfo // relationships to update (others left unchanged)
	newURLs     []entityInfo
	editNote    string // https://musicbrainz.org/doc/Edit_Note
}

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

var urlFuncs = map[*regexp.Regexp]urlFunc{
	// MBBE-71: Normalize Tidal streaming URLs:
	//  https://listen.tidal.com/album/114997210 -> https://tidal.com/album/114997210
	//  https://listen.tidal.com/artist/11069    -> https://tidal.com/artist/11069
	//  https://tidal.com/browse/album/11103031  -> https://tidal.com/album/11103031
	//  https://tidal.com/browse/artist/5015356  -> https://tidal.com/artist/5015356
	//  https://tidal.com/browse/track/120087531 -> https://tidal.com/track/120087531
	//  (and many other forms)
	regexp.MustCompile(`^https?://` + // both http:// and https://
		`(?:(?:desktop\.|desktop\.stage\.|listen\.|www\.)?tidal\.com)` + // hostname
		`(?:/browse)?` + // optional /browse component
		`(/(?:album|artist|track|video|album/\d+/track)/\d+)` + // match significant components, e.g. /album/123
		`(?:/|\?.*)?` + // trailing slash or query
		`$`): func(orig *entityInfo, ms []string) *urlResult {
		p := ms[1]
		res := urlResult{
			rewritten: "https://tidal.com" + p,
			editNote:  tidalEditNote,
		}

		// If the URL contains both an album and a track, use its relationships to
		// figure out what it should actually be.
		if ms := tidalAlbumTrackRegexp.FindStringSubmatch(p); ms != nil {
			album, track := ms[1], ms[2]
			if len(filterRels(orig.rels, "recording")) > 0 {
				res.rewritten = "https://tidal.com/track/" + track
			} else if len(filterRels(orig.rels, "release")) > 0 {
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
		`$`): func(orig *entityInfo, ms []string) *urlResult {
		res := urlResult{
			rewritten: orig.name, // leave the URL alone
			editNote:  geocitiesEditNote,
		}
		endDate := geocitiesEndDate
		if ms[1] == "jp" || ms[1] == "co.jp" {
			endDate = geocitiesJapanEndDate
		}
		for _, rel := range orig.rels {
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
		`$`): func(orig *entityInfo, ms []string) *urlResult {
		res := urlResult{
			rewritten: orig.name, // leave the URL alone
			editNote:  tidalStoreEditNote,
		}
		// TODO: The server returns an error if an edit would create a duplicate relationship
		// (i.e. same link type, source, and target). This code could try to handle that case, but
		// I've instead manually created edits to clean up the few URLs with multiple relationships.
		for _, rel := range orig.rels {
			old := rel
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
			if rel != old {
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
		`$`): func(orig *entityInfo, ms []string) *urlResult {
		if len(orig.rels) == 0 {
			return nil
		}

		res := urlResult{
			rewritten: orig.name, // leave the URL alone
			editNote:  recmusicEditNote,
		}

		newURL := entityInfo{
			name: fmt.Sprintf("https://music.tower.jp/%s/detail/%s", ms[1], ms[2]),
			typ:  urlType,
		}
		for _, rel := range orig.rels {
			old := rel
			if !rel.ended {
				rel.ended = true
				rel.endDate = recmusicEndDate
			}
			if rel != old {
				res.updatedRels = append(res.updatedRels, rel)
			}

			if _, ok := missingTowerRecordsPairs[[2]string{ms[1], ms[2]}]; !ok {
				newRel := old
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
