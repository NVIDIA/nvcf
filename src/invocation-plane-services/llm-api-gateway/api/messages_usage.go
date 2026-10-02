// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import "encoding/json"

// This incremental JSON parser validates structure while skipping content
// strings without allocating them. Only root keys and the root usage value
// are buffered. A large message therefore does not hide its trailing usage.
type messagesJSONUsageScanner struct {
	states                            [256]byte
	depth                             int
	started, done, invalid, oversized bool
	token                             byte
	keyToken, escaped                 bool
	unicode                           int
	text                              []byte
	key                               string
	capture                           bool
	raw                               []byte
	seenUsage                         bool
	usage                             messagesUsage
}

const (
	jsonObjectKey byte = iota
	jsonObjectColon
	jsonObjectValue
	jsonObjectComma
	jsonObjectNextKey
	jsonArrayValue
	jsonArrayComma
	jsonArrayNextValue
)

func jsonSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\r' || b == '\t' }

func (s *messagesJSONUsageScanner) Write(p []byte) {
	for _, b := range p {
		s.byte(b)
	}
}

func (s *messagesJSONUsageScanner) appendUsage(b byte) {
	if !s.capture {
		return
	}
	if len(s.raw) == messagesUsageBufferLimit {
		s.oversized = true
		s.capture = false
		s.raw = nil
		return
	}
	s.raw = append(s.raw, b)
}

func (s *messagesJSONUsageScanner) valueDone() {
	if s.depth == 0 {
		s.done = true
		return
	}
	if s.depth == 1 && s.capture {
		if s.seenUsage || json.Unmarshal(s.raw, &s.usage) != nil {
			s.invalid = true
		}
		s.seenUsage = true
		s.capture = false
		s.raw = nil
	}
	switch s.states[s.depth-1] {
	case jsonObjectValue:
		s.states[s.depth-1] = jsonObjectComma
	case jsonArrayValue, jsonArrayNextValue:
		s.states[s.depth-1] = jsonArrayComma
	default:
		s.invalid = true
	}
}

func (s *messagesJSONUsageScanner) byte(b byte) {
	if s.invalid {
		return
	}
	if s.token == 'p' {
		if b != ',' && b != '}' && b != ']' && !jsonSpace(b) {
			if len(s.text) >= 64 {
				s.invalid = true
				return
			}
			s.text = append(s.text, b)
			s.appendUsage(b)
			return
		}
		if !json.Valid(s.text) {
			s.invalid = true
			return
		}
		s.token = 0
		s.text = nil
		s.valueDone()
		if s.invalid {
			return
		}
	}
	if s.token == '"' {
		s.appendUsage(b)
		if s.keyToken && s.depth == 1 {
			if len(s.text) >= 1024 {
				s.invalid = true
				return
			}
			s.text = append(s.text, b)
		}
		if s.unicode > 0 {
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
				s.invalid = true
			}
			s.unicode--
			return
		}
		if s.escaped {
			s.escaped = false
			switch b {
			case 'u':
				s.unicode = 4
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			default:
				s.invalid = true
			}
			return
		}
		switch b {
		case '\\':
			s.escaped = true
		case '"':
			s.token = 0
			if s.keyToken {
				if s.depth == 1 && json.Unmarshal(s.text, &s.key) != nil {
					s.invalid = true
				}
				s.text = nil
				s.states[s.depth-1] = jsonObjectColon
			} else {
				s.valueDone()
			}
		default:
			if b < 0x20 {
				s.invalid = true
			}
		}
		return
	}
	if s.done {
		if !jsonSpace(b) {
			s.invalid = true
		}
		return
	}
	if !s.started {
		if jsonSpace(b) {
			return
		}
		if b != '{' {
			s.invalid = true
			return
		}
		s.started = true
		s.depth = 1
		s.states[0] = jsonObjectKey
		return
	}
	if s.depth == 0 {
		s.invalid = true
		return
	}
	state := s.states[s.depth-1]
	if jsonSpace(b) {
		s.appendUsage(b)
		return
	}
	switch state {
	case jsonObjectKey, jsonObjectNextKey:
		if b == '}' && state == jsonObjectKey {
			s.appendUsage(b)
			s.depth--
			s.valueDone()
			return
		}
		if b != '"' {
			s.invalid = true
			return
		}
		s.appendUsage(b)
		s.token = '"'
		s.keyToken = true
		if s.depth == 1 {
			s.text = []byte{'"'}
		}
		return
	case jsonObjectColon:
		s.appendUsage(b)
		if b != ':' {
			s.invalid = true
			return
		}
		s.states[s.depth-1] = jsonObjectValue
		return
	case jsonObjectComma, jsonArrayComma:
		s.appendUsage(b)
		if b == ',' {
			if state == jsonObjectComma {
				s.states[s.depth-1] = jsonObjectNextKey
			} else {
				s.states[s.depth-1] = jsonArrayNextValue
			}
			return
		}
		if state == jsonObjectComma && b == '}' || state == jsonArrayComma && b == ']' {
			s.depth--
			s.valueDone()
			return
		}
		s.invalid = true
		return
	case jsonArrayValue:
		if b == ']' {
			s.appendUsage(b)
			s.depth--
			s.valueDone()
			return
		}
	}
	if s.depth == 1 && state == jsonObjectValue && s.key == "usage" {
		if s.seenUsage {
			s.invalid = true
			return
		}
		s.capture = true
	}
	s.appendUsage(b)
	switch b {
	case '{', '[':
		if s.depth == len(s.states) {
			s.invalid = true
			return
		}
		if b == '{' {
			s.states[s.depth] = jsonObjectKey
		} else {
			s.states[s.depth] = jsonArrayValue
		}
		s.depth++
	case '"':
		s.token = '"'
		s.keyToken = false
	case 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		s.token = 'p'
		s.text = []byte{b}
	default:
		s.invalid = true
	}
}
