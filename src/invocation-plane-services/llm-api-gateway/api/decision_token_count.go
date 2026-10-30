// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// maxDecisionBodyDepth bounds JSON nesting in a decision request body.
const maxDecisionBodyDepth = 64

var errDecisionBodyTooDeep = errors.New("request body is nested more than 64 levels deep")

// estimatedTokenCountForDecisionBody estimates the input tokens of a decision
// route request without knowing its schema. Every string leaf counts as text.
// Each image slot counts a flat imageTokens and its payload is never counted,
// so a large base64 image cannot hide or inflate the estimate. Image slots are
// objects with type "input_image", each element of an "images" array, each
// value of an "assets" object, and any string that is a base64 data URL.
//
// The body is read in one pass over decoder tokens with a stack of at most
// maxDecisionBodyDepth frames, so time is linear and extra memory does not
// grow with nesting. Deeper bodies return an error.
func estimatedTokenCountForDecisionBody(raw []byte, imageTokens int) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if !json.Valid(trimmed) {
		return estimatedTokenCountForText(string(raw)), nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var stack []decisionFrame
	root := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return root, nil
			}
			return 0, err
		}
		var (
			value int
			done  bool
		)
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				frame := decisionFrame{object: t == '{'}
				if len(stack) > 0 {
					top := &stack[len(stack)-1]
					if top.slots {
						// The whole element is one image slot: skip it.
						if err := skipContainer(dec, len(stack)+1); err != nil {
							return 0, err
						}
						top.total += imageTokens
						top.haveKey = false
						continue
					}
					frame.slots = (top.key == "images" && t == '[') || (top.key == "assets" && t == '{')
				}
				if len(stack) >= maxDecisionBodyDepth {
					return 0, errDecisionBodyTooDeep
				}
				stack = append(stack, frame)
				continue
			default: // closing delimiter
				frame := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				value, done = frame.total, true
				if frame.image {
					value = imageTokens
				}
			}
		case string:
			top := (*decisionFrame)(nil)
			if len(stack) > 0 {
				top = &stack[len(stack)-1]
			}
			if top != nil && top.object && !top.haveKey {
				top.key, top.haveKey = t, true
				continue
			}
			switch {
			case top != nil && top.slots, isBase64DataURL(t):
				value = imageTokens
			default:
				value = estimatedTokenCountForText(t)
			}
			if top != nil && top.object && top.key == "type" && t == "input_image" {
				top.image = true
			}
			done = true
		default: // number, bool, null
			if len(stack) > 0 && stack[len(stack)-1].slots {
				value = imageTokens
				done = true
			} else {
				done = true
			}
		}
		if !done {
			continue
		}
		if len(stack) == 0 {
			root += value
			continue
		}
		top := &stack[len(stack)-1]
		top.total += value
		top.haveKey = false
	}
}

// decisionFrame is the state of one open object or array.
type decisionFrame struct {
	object  bool
	haveKey bool   // an object key was read and its value is next
	key     string // the current object key
	slots   bool   // each child is an image slot
	image   bool   // this object has type "input_image"
	total   int
}

// skipContainer consumes tokens up to the close of the container whose opening
// delimiter was just read, enforcing the depth cap.
func skipContainer(dec *json.Decoder, depth int) error {
	for open := 1; open > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				open++
				if depth+open-1 > maxDecisionBodyDepth {
					return errDecisionBodyTooDeep
				}
			default:
				open--
			}
		}
	}
	return nil
}

func isBase64DataURL(text string) bool {
	return strings.HasPrefix(text, "data:") && strings.Contains(text, ";base64,")
}
