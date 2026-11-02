// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	echo "github.com/labstack/echo/v4"
)

// decisionModelMembers holds the byte ranges of the values of the top-level
// "model" members of a decision request body.
type decisionModelMembers struct {
	values [][2]int
}

// scanDecisionModelMembers finds the top-level "model" members of raw, which
// must already be valid JSON. It reads the body once without copying member
// values. A key that decodes to "model" counts, including an escaped spelling
// such as "model". It returns the same 400 as rejectAmbiguousMembers when
// "model" is spelled with more than one letter case, since the gateway would
// route one value while the backend reads another.
func scanDecisionModelMembers(raw []byte) (decisionModelMembers, error) {
	var members decisionModelMembers
	i := skipJSONSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return members, nil
	}
	spelling := ""
	i++
	for {
		i = skipJSONSpace(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			return members, nil
		}
		if raw[i] == ',' {
			i = skipJSONSpace(raw, i+1)
		}
		keyStart := i
		i = skipJSONString(raw, i)
		key := decodeJSONKey(raw[keyStart:i])
		i = skipJSONSpace(raw, i)
		i = skipJSONSpace(raw, i+1) // the colon
		valueStart := i
		i = skipJSONValue(raw, i)
		if !strings.EqualFold(key, "model") {
			continue
		}
		if spelling != "" && key != spelling {
			return members, echo.NewHTTPError(http.StatusBadRequest,
				`field "model" is sent more than once with different letter case`)
		}
		spelling = key
		if key == "model" {
			members.values = append(members.values, [2]int{valueStart, i})
		}
	}
}

// splice returns raw with each top-level model value replaced by model. Every
// other byte is kept. raw is returned as is when there is no model member.
func (m decisionModelMembers) splice(raw []byte, model string) []byte {
	if len(m.values) == 0 {
		return raw
	}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(model) // a string always encodes
	value := bytes.TrimRight(encoded.Bytes(), "\n")

	size := len(raw)
	for _, r := range m.values {
		size += len(value) - (r[1] - r[0])
	}
	out := make([]byte, 0, size)
	last := 0
	for _, r := range m.values {
		out = append(out, raw[last:r[0]]...)
		out = append(out, value...)
		last = r[1]
	}
	return append(out, raw[last:]...)
}

func decodeJSONKey(quoted []byte) string {
	if bytes.IndexByte(quoted, '\\') < 0 {
		return string(quoted[1 : len(quoted)-1])
	}
	var key string
	_ = json.Unmarshal(quoted, &key)
	return key
}

func skipJSONSpace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipJSONString returns the index after the string that starts at raw[i].
func skipJSONString(raw []byte, i int) int {
	for i++; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return i
}

// skipJSONValue returns the index after the value that starts at raw[i].
func skipJSONValue(raw []byte, i int) int {
	depth := 0
	for i < len(raw) {
		switch raw[i] {
		case '"':
			i = skipJSONString(raw, i)
			if depth == 0 {
				return i
			}
			continue
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
			if depth == 0 {
				return i + 1
			}
		case ',', ' ', '\t', '\n', '\r':
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return i
}
