// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// sliceContains returns true if vals contains s.
func sliceContains(vals []string, s string) bool {
	for _, v := range vals {
		if v == s {
			return true
		}
	}
	return false
}

// readLine reads the next line from sc.
// If an error was encountered (possibly during an earlier read), it is returned.
// After all lines have been read successfully, io.EOF is returned.
func readLine(sc *bufio.Scanner) (string, error) {
	if sc.Scan() {
		return sc.Text(), nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

// readInt is a wrapper around readLine that converts lines to ints.
func readInt(sc *bufio.Scanner) (int, error) {
	ln, err := readLine(sc)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(ln)
}

// readMBID is a wrapper around readLine that checks that lines contain valid UUIDs.
func readMBID(sc *bufio.Scanner) (string, error) {
	ln, err := readLine(sc)
	if err != nil {
		return "", err
	}
	if !mbidRegexp.MatchString(ln) {
		return "", fmt.Errorf("invalid MBID %q", ln)
	}
	return ln, nil
}

// mbidRegexp matches a MusicBrainz ID (i.e. a UUID).
var mbidRegexp = regexp.MustCompile(
	`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
