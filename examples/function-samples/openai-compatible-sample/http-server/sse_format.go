// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"net/http"
)

// SSE line endings selectable with x_load_tester_sse_line_ending. The SSE
// specification allows all three; the default is the one the rest of the
// sample writes (lf).
const (
	sseLineEndingLF   = "lf"
	sseLineEndingCRLF = "crlf"
	sseLineEndingCR   = "cr"
)

func validSSELineEnding(value string) bool {
	switch value {
	case sseLineEndingLF, sseLineEndingCRLF, sseLineEndingCR:
		return true
	}
	return false
}

// sseFormatWriter rewrites the line endings of a Messages stream and writes it
// in fixed-size pieces, flushing after each one, so a multi-byte UTF-8 sequence
// can be split across two writes. Structural SSE newlines are raw 0x0A bytes;
// newlines inside text deltas are JSON-escaped, so replacing 0x0A never touches
// content.
type sseFormatWriter struct {
	http.ResponseWriter
	ending []byte
	split  int
}

// newSSEFormatWriter returns w unchanged when neither control is set, so the
// default path is exactly the previous behavior.
func newSSEFormatWriter(w http.ResponseWriter, lineEnding string, splitBytes int) http.ResponseWriter {
	var ending []byte
	switch lineEnding {
	case sseLineEndingCRLF:
		ending = []byte("\r\n")
	case sseLineEndingCR:
		ending = []byte("\r")
	}
	if ending == nil && splitBytes <= 0 {
		return w
	}
	return &sseFormatWriter{ResponseWriter: w, ending: ending, split: splitBytes}
}

func (s *sseFormatWriter) Write(p []byte) (int, error) {
	out := p
	if s.ending != nil {
		out = bytes.ReplaceAll(p, []byte("\n"), s.ending)
	}
	for len(out) > 0 {
		n := len(out)
		if s.split > 0 && n > s.split {
			n = s.split
		}
		written, err := s.ResponseWriter.Write(out[:n])
		if err != nil {
			return 0, err
		}
		out = out[written:]
		if s.split > 0 {
			flush(s.ResponseWriter)
		}
	}
	// The caller wrote len(p) bytes of its frame, whatever the rewritten length.
	return len(p), nil
}

func (s *sseFormatWriter) Flush() {
	flush(s.ResponseWriter)
}
