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

	// compiled holds the pre-compiled pattern for "regex" rules so a rule
	// is validated at Insert time and queries never hit a panicking
	// regexp.MustCompile. It is nil for non-regex rules.
	compiled *regexp.Regexp
}

type List struct {
	DataList []Data
}

func (s *List) Insert(str string) error {
	// SplitN so a regex rule containing colons (e.g. "regex:^https?://")
	// is not cut into too many pieces and rejected.
	kv := strings.SplitN(str, ":", 2)

	switch len(kv) {
	case 1:
		s.DataList = append(s.DataList,
			Data{
				Type:    "domain",
				Content: strings.ToLower(kv[0])})
	case 2:
		ruleType := strings.ToLower(kv[0])
		content := strings.ToLower(kv[1])
		if ruleType == "regex" {
			re, err := regexp.Compile(content)
			if err != nil {
				return fmt.Errorf("invalid regex rule %q: %w", str, err)
			}
			s.DataList = append(s.DataList,
				Data{Type: ruleType, Content: content, compiled: re})
			return nil
		}
		s.DataList = append(s.DataList,
			Data{Type: ruleType, Content: content})
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
					// The suffix starts mid-label ("notexample.com" for
					// "example.com"); it is not a subdomain of this rule,
					// so keep scanning the remaining rules.
					continue
				}
				return true
			}
		case "regex":
			// Regex rules keep their original case semantics; write
			// patterns in lower case or use (?i) if case-insensitive.
			// compiled is populated at Insert time; a nil guard keeps the
			// hot path panic-free even if a rule list was built elsewhere.
			if data.compiled == nil {
				continue
			}
			if data.compiled.MatchString(str) {
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
