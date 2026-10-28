package catalog

import (
	"bytes"
	"sort"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func sortStrings(s []string) { sort.Strings(s) }
