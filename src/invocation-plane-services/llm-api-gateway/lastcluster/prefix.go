/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lastcluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Endpoints that derive payload session values.
const (
	EndpointChatCompletions = "chat_completions"
	EndpointResponses       = "responses"
	EndpointMessages        = "messages"
)

const (
	payloadValuePrefix = "pc:v1:"
	payloadHashVersion = "nvcf-lc-payload-v1"
)

// leadingSlots is the number of leading parts each endpoint hashes.
var leadingSlots = map[string]int{
	EndpointChatCompletions: 0,
	EndpointMessages:        1,
	EndpointResponses:       2,
}

// PayloadInput is the raw request material for a payload session.
type PayloadInput struct {
	Endpoint  string
	OrgID     string
	ProjectID string
	// Leading holds fixed slots hashed before the items: none for chat,
	// [system] for messages, [instructions, prompt] for responses. A nil
	// entry means the part is absent.
	Leading []json.RawMessage
	Items   []json.RawMessage
	// Output marks model-output items. It must have the same length as Items.
	Output []bool
}

// PayloadSessionValues returns the lookup and write session values for a
// payload session. write covers the whole conversation. lookup covers the
// conversation before the last contiguous run of model output, so for an
// append-only client it equals the previous turn's write value. lookup is
// empty when no item is model output or the run starts at the first item.
//
// Both values are "pc:v1:" plus the hex SHA-256 of one stream: the version
// tag, endpoint, OrgID, and ProjectID; then per leading slot a presence byte
// and, when present, the part; then each item. Every string and JSON part is
// prefixed with its uint64 big-endian length. JSON parts are normalized by
// decoding with UseNumber and re-encoding, which sorts object keys and drops
// insignificant whitespace but keeps number literals as sent. lookup is the
// digest taken at the cut; write is the final digest.
func PayloadSessionValues(in PayloadInput) (lookup, write string, err error) {
	slots, ok := leadingSlots[in.Endpoint]
	if !ok {
		return "", "", fmt.Errorf("unknown payload endpoint %q", in.Endpoint)
	}
	if len(in.Leading) != slots {
		return "", "", fmt.Errorf("endpoint %s has %d leading parts, got %d", in.Endpoint, slots, len(in.Leading))
	}
	if len(in.Output) != len(in.Items) {
		return "", "", fmt.Errorf("output flags length %d does not match items length %d", len(in.Output), len(in.Items))
	}

	h := sha256.New()
	for _, part := range []string{payloadHashVersion, in.Endpoint, in.OrgID, in.ProjectID} {
		writeFrame(h, []byte(part))
	}
	for i, part := range in.Leading {
		if part == nil {
			_, _ = h.Write([]byte{0})
			continue
		}
		canonical, err := canonicalJSON(part)
		if err != nil {
			return "", "", fmt.Errorf("leading part %d: %w", i, err)
		}
		_, _ = h.Write([]byte{1})
		writeFrame(h, canonical)
	}

	cut := lastOutputRunStart(in.Output)
	for i, item := range in.Items {
		if i == cut && cut > 0 {
			lookup = payloadValuePrefix + hex.EncodeToString(h.Sum(nil))
		}
		canonical, err := canonicalJSON(item)
		if err != nil {
			return "", "", fmt.Errorf("item %d: %w", i, err)
		}
		writeFrame(h, canonical)
	}
	return lookup, payloadValuePrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// lastOutputRunStart returns the index of the first item in the last
// contiguous run of model output, or -1 when no item is model output.
func lastOutputRunStart(output []bool) int {
	i := len(output) - 1
	for i >= 0 && !output[i] {
		i--
	}
	if i < 0 {
		return -1
	}
	for i > 0 && output[i-1] {
		i--
	}
	return i
}

func writeFrame(h hash.Hash, part []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(part)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(part)
}

// canonicalJSON re-encodes one JSON value deterministically.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON: trailing data")
	}
	return json.Marshal(value)
}
