/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */

package full

import "strings"

type Map struct {
	DataMap map[string][]string
}

func (m *Map) Insert(k string, v string) error {
	// DNS names are case-insensitive, so hosts entries are keyed in lower case.
	k = strings.ToLower(k)
	if m.DataMap[k] == nil {
		m.DataMap[k] = []string{v}
	} else {
		m.DataMap[k] = append(m.DataMap[k], v)
	}
	return nil
}

func (m *Map) Get(k string) []string {
	return m.DataMap[strings.ToLower(k)]
}

func (m *Map) Name() string {
	return "full-map"
}
