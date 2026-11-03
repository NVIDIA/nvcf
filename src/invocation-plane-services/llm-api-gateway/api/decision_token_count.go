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
// each element of an "images" array, each value of an "assets" object, and any
// string that is a base64 image data URL (see isBase64ImageDataURL).
//
// A nested object with type "input_image" counts as one image in place of its
// image_url and base64 fields; its other strings still count as text. The root
// object is never an image, so a field the backend ignores cannot turn the
// whole prompt into one image.
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
				if frame.image && len(stack) > 0 {
					value = frame.total - frame.imageFieldTotal + imageTokens
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
			case top != nil && top.slots, isBase64ImageDataURL(t):
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
		if top.object && isImageField(top.key) {
			top.imageFieldTotal += value
		}
		top.haveKey = false
	}
}

// isImageField reports whether key holds the image of an input_image object.
func isImageField(key string) bool {
	return key == "image_url" || key == "base64"
}

// decisionFrame is the state of one open object or array.
type decisionFrame struct {
	object  bool
	haveKey bool   // an object key was read and its value is next
	key     string // the current object key
	slots   bool   // each child is an image slot
	image   bool   // this object has type "input_image"
	total   int
	// imageFieldTotal is the part of total from image_url and base64 values,
	// replaced by one image when the object is an input_image.
	imageFieldTotal int
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

// maxImageSubtypeLength bounds the media subtype of an image data URL.
const maxImageSubtypeLength = 64

// isBase64ImageDataURL reports whether text is a base64 image data URL:
// "data:image/", a subtype of 1 to 64 characters from [A-Za-z0-9.+-] (a
// subset of the RFC 6838 restricted name characters), ";base64,", and then
// only base64 characters (standard or URL-safe alphabet, with = padding).
// Prose in the subtype or behind the prefix is text, so neither can make a
// prompt count as one image. It reads text once and does not allocate.
func isBase64ImageDataURL(text string) bool {
	const prefix, marker = "data:image/", ";base64,"
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := text[len(prefix):]
	subtype := 0
	for subtype < len(rest) && subtype <= maxImageSubtypeLength && isImageSubtypeByte(rest[subtype]) {
		subtype++
	}
	if subtype == 0 || subtype > maxImageSubtypeLength || !strings.HasPrefix(rest[subtype:], marker) {
		return false
	}
	for _, b := range []byte(rest[subtype+len(marker):]) {
		if !isBase64Byte(b) {
			return false
		}
	}
	return true
}

func isImageSubtypeByte(b byte) bool {
	switch {
	case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9':
		return true
	default:
		return b == '.' || b == '+' || b == '-'
	}
}

func isBase64Byte(b byte) bool {
	switch {
	case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9':
		return true
	default:
		return b == '+' || b == '/' || b == '=' || b == '-' || b == '_'
	}
}
