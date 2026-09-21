/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */

package mix

import (
	"fmt"
	"regexp"
	"strings"
)

type Data struct {
	Type    string
	Content string
}

type List struct {
	DataList []Data
}

func (s *List) Insert(str string) error {
	kv := strings.Split(str, ":")

	switch len(kv) {
	case 1:
		s.DataList = append(s.DataList,
			Data{
				Type:    "domain",
				Content: strings.ToLower(kv[0])})
	case 2:
		s.DataList = append(s.DataList,
			Data{
				Type:    strings.ToLower(kv[0]),
				Content: strings.ToLower(kv[1])})
	default:
		return fmt.Errorf("invalid format: %s", str)
	}

	return nil
}

func (s *List) Has(str string) bool {
	// DNS names are case-insensitive; rules are stored in lower case at
	// Insert time, so queries are compared in lower case as well.
	lower := strings.ToLower(str)
	for _, data := range s.DataList {
		switch data.Type {
		case "domain":
			idx := len(lower) - len(data.Content)
			if idx >= 0 && data.Content == lower[idx:] {
				if idx >= 1 && (lower[idx-1] != '.') {
					return false
				}
				return true
			}
		case "regex":
			// Regex rules keep their original case semantics; write
			// patterns in lower case or use (?i) if case-insensitive.
			reg := regexp.MustCompile(data.Content)
			if reg.MatchString(str) {
				return true
			}
		case "keyword":
			if strings.Contains(lower, data.Content) {
				return true
			}
		case "full":
			if data.Content == lower {
				return true
			}
		}
	}
	return false
}

func (s *List) Name() string {
	return "mix-list"
}
