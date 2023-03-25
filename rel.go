// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// relInfo describes a relationship between one entity and another.
type relInfo struct {
	id         int    // database ID of link itself
	linkTypeID int    // database ID of link type, e.g. 978 for artist streaming page
	linkPhrase string // e.g. "has a fan page at"
	beginDate  date
	endDate    date
	ended      bool
	backward   bool
	targetMBID string
	targetName string
	targetType string // entity type, e.g. "artist", "release", "recording"
}

// desc returns a string describing the relationship belonging to name,
// e.g. "[name] has an official homepage at [url]".
func (rel *relInfo) desc(name string) string {
	target := rel.targetName
	if target == "" {
		target = rel.targetMBID
	}
	phrase := rel.linkPhrase + fmt.Sprintf("[%d]", rel.linkTypeID)

	var s string
	if rel.backward {
		s = fmt.Sprintf("%s %s %s", target, phrase, name)
	} else {
		s = fmt.Sprintf("%s %s %s", name, phrase, target)
	}
	if !rel.beginDate.empty() {
		s += fmt.Sprintf(" from %04d-%02d-%02d", rel.beginDate.year, rel.beginDate.month, rel.beginDate.day)
	}
	if rel.ended {
		s += fmt.Sprintf(" until %04d-%02d-%02d", rel.endDate.year, rel.endDate.month, rel.endDate.day)
	}
	return s
}

// filterRels returns relationships to targets of the specified type, e.g. "artist".
func filterRels(rels []relInfo, entityType string) []relInfo {
	var filtered []relInfo
	for _, rel := range rels {
		if rel.targetType == entityType {
			filtered = append(filtered, rel)
		}
	}
	return filtered
}

type date struct{ year, month, day int }

func (d *date) empty() bool { return d.year == 0 && d.month == 0 && d.day == 0 }

// setRelEditVals sets values needed by the /relationship-editor endpoint.
// pre is prepended to each parameter name and should be e.g. "rel-editor.rels.0".
// If orig is non-nil, an "edit" request is set with differences between orig and rel.
// If orig is nil, an "add" request is set to create a new relationship.
// The caller must set entity-related parameters when creating new relationships.
func setRelEditVals(vals map[string]string, pre string, rel relInfo, orig *relInfo) error {
	if orig == nil {
		if rel.id != 0 {
			return fmt.Errorf("invalid rel %d", rel.id)
		}
	} else if rel == *orig {
		return fmt.Errorf("no changes for rel %d", rel.id)
	}

	// These parameters are handled by lib/MusicBrainz/Server/Controller/RelationshipEditor.pm.
	origCnt := len(vals)
	linkTypeKey := pre + "link_type"
	if orig == nil || rel.linkTypeID != orig.linkTypeID {
		vals[linkTypeKey] = strconv.Itoa(rel.linkTypeID)
	}

	itoaNonZero := func(v int) string {
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	}

	if !rel.beginDate.empty() && (orig == nil || rel.beginDate != orig.beginDate) {
		vals[pre+"period.begin_date.year"] = itoaNonZero(rel.beginDate.year)
		vals[pre+"period.begin_date.month"] = itoaNonZero(rel.beginDate.month)
		vals[pre+"period.begin_date.day"] = itoaNonZero(rel.beginDate.day)
	}
	if !rel.endDate.empty() && (orig == nil || rel.endDate != orig.endDate) {
		vals[pre+"period.end_date.year"] = itoaNonZero(rel.endDate.year)
		vals[pre+"period.end_date.month"] = itoaNonZero(rel.endDate.month)
		vals[pre+"period.end_date.day"] = itoaNonZero(rel.endDate.day)
	}
	if (orig == nil && rel.ended) || (orig != nil && rel.ended != orig.ended) {
		vals[pre+"period.ended"] = boolToParam(rel.ended)
	}
	if len(vals) == origCnt {
		return fmt.Errorf("unsupported update for rel (%+v)", rel)
	}

	// The server returns a 400 error if "action" and "link_type" are omitted.
	if orig == nil {
		vals[pre+"action"] = "add"
	} else {
		vals[pre+"action"] = "edit"
		vals[pre+"id"] = strconv.Itoa(rel.id)
		if _, ok := vals[linkTypeKey]; !ok {
			vals[linkTypeKey] = strconv.Itoa(rel.linkTypeID)
		}
	}

	return nil
}

// postRelEdit posts vals to /relationship-editor.
// IDs of created relationships are returned.
// If existing relationships are edited, IDs are 0.
func postRelEdit(ctx context.Context, srv *server, vals map[string]string,
	editNote string, makeVotable bool) ([]int, error) {
	// Set additional parameters.
	vals["rel-editor.edit_note"] = editNote
	if makeVotable {
		vals["rel-editor.make_votable"] = "1"
	}

	b, err := srv.post(ctx, "/relationship-editor", vals)
	if err != nil {
		return nil, fmt.Errorf("%v (%q)", err, b)
	}
	// The response is written by submit_edits in lib/MusicBrainz/Server/Controller/WS/js/Edit.pm,
	// which oddly doesn't include the actual edit IDs.
	var data struct {
		Edits []struct {
			RelationshipID int `json:"relationship_id"`
			EditType       int `json:"edit_type"`
			Response       int `json:"response"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %v", err)
	}
	var ids []int
	for i, ed := range data.Edits {
		if ed.Response != 1 {
			return ids, fmt.Errorf("relationship edit %v with type %v failed: %v", i, ed.EditType, ed.Response)
		}
		ids = append(ids, ed.RelationshipID)
	}
	return ids, nil
}

// boolToParam returns a string corresponding to v to use as a parameter passed to MusicBrainz.
// boolean_from_json() in lib/MusicBrainz/Server/Data/Utils.pm seems to regrettably use Perl truthiness.
func boolToParam(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
