// Copyright 2023 Daniel Erat.
// All rights reserved.

package main

import (
	"bufio"
	"os"
	"strings"
)

// readTable opens the named dump file and passes each row to fn.
func readTable(p string, fn func([]string)) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		row := strings.Split(sc.Text(), "\t")
		for i, v := range row {
			if v == `\N` { // null
				v = ""
			}
			row[i] = v
		}
		fn(row)
	}
	return sc.Err()
}
