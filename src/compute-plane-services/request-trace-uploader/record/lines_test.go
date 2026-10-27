// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// member compresses lines into one gzip member, as one Dynamo flush does.
func member(t *testing.T, lines ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeRaw(t *testing.T, parts ...[]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, bytes.Join(parts, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noisy returns a line that does not compress to almost nothing, so cutting a
// member in half lands inside its compressed data.
func noisy(tag string, n int) string {
	var b strings.Builder
	b.WriteString(`{"tag":"` + tag + `","pad":"`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%x", (i*2654435761)%4093)
	}
	b.WriteString(`"}`)
	return b.String()
}

type lineResult struct {
	lines []string
	stats LineStats
	sum   []byte
	err   error
}

func readLines(t *testing.T, path string, opts LineOptions) lineResult {
	t.Helper()
	lines, err := OpenLines(path, opts)
	if err != nil {
		t.Fatalf("OpenLines() error = %v", err)
	}
	defer lines.Close()
	var got []string
	for lines.Next() {
		got = append(got, string(lines.Line()))
	}
	return lineResult{lines: got, stats: lines.Stats(), sum: lines.Sum(), err: lines.Err()}
}

func fileSum(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

func TestLinesFingerprintEqualsFileHash(t *testing.T) {
	first := member(t, noisy("a", 40), noisy("b", 40))
	second := member(t, noisy("c", 40))
	third := member(t, noisy("d", 40))
	tests := []struct {
		name  string
		parts [][]byte
	}{
		{name: "clean", parts: [][]byte{first, second, third}},
		{name: "truncated tail", parts: [][]byte{first, second, third[:len(third)/2]}},
		{name: "partial member mid-file", parts: [][]byte{first, second[:len(second)/2], third}},
		{name: "empty", parts: nil},
		{name: "not gzip", parts: [][]byte{[]byte("plain text\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeRaw(t, tt.parts...)
			got := readLines(t, path, LineOptions{})
			if got.err != nil {
				t.Fatalf("Err() = %v", got.err)
			}
			if want := fileSum(t, path); !bytes.Equal(got.sum, want) {
				t.Fatalf("Sum() = %x, want %x", got.sum, want)
			}
		})
	}
}

func TestLinesKeepsRecordsBeforeTruncatedTail(t *testing.T) {
	third := member(t, noisy("c", 60))
	path := writeRaw(t, member(t, "one", "two"), member(t, "three"), third[:len(third)/2])
	var rejects bytes.Buffer

	got := readLines(t, path, LineOptions{Reject: &rejects})
	if got.err != nil {
		t.Fatalf("Err() = %v", got.err)
	}
	if !slices.Equal(got.lines, []string{"one", "two", "three"}) {
		t.Fatalf("lines = %q, want the records of the two complete members", got.lines)
	}
	if got.stats.LostBytes == 0 {
		t.Errorf("lost bytes = 0, want the cut member counted")
	}
	if got.stats.Cut > 1 {
		t.Errorf("cut = %d, want at most the one partial line", got.stats.Cut)
	}
	if got.stats.Cut == 1 && !strings.HasPrefix(rejects.String(), `{"tag":"c"`) {
		t.Errorf("reject = %.40q, want the partial line's raw bytes", rejects.String())
	}
}

func TestLinesResyncsAfterPartialMemberMidFile(t *testing.T) {
	second := member(t, noisy("b", 60))
	path := writeRaw(t, member(t, "one"), second[:len(second)/2], member(t, "three", "four"))

	got := readLines(t, path, LineOptions{})
	if got.err != nil {
		t.Fatalf("Err() = %v", got.err)
	}
	var kept []string
	for _, line := range got.lines {
		if line == "one" || line == "three" || line == "four" {
			kept = append(kept, line)
		}
	}
	if !slices.Equal(kept, []string{"one", "three", "four"}) {
		t.Fatalf("lines = %q, want one, three and four around the partial member", got.lines)
	}
	if got.stats.Resyncs != 1 || got.stats.LostBytes == 0 {
		t.Errorf("resyncs = %d lost bytes = %d, want 1 and more than 0", got.stats.Resyncs, got.stats.LostBytes)
	}
}

func TestLinesDrainsOversizedLineToReject(t *testing.T) {
	long := strings.Repeat("x", 100)
	path := writeRaw(t, member(t, "before", long, "after"))
	var rejects bytes.Buffer

	got := readLines(t, path, LineOptions{MaxLineBytes: 32, Reject: &rejects})
	if got.err != nil {
		t.Fatalf("Err() = %v", got.err)
	}
	if !slices.Equal(got.lines, []string{"before", "after"}) {
		t.Fatalf("lines = %q, want before and after", got.lines)
	}
	if got.stats.Oversized != 1 {
		t.Errorf("oversized = %d, want 1", got.stats.Oversized)
	}
	if rejects.String() != long+"\n" {
		t.Errorf("reject = %q, want the whole raw line", rejects.String())
	}
}

func TestLinesSplitsOnEveryNewlineFormLikeBash(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("a\rb\r\nc\n\n\r\nd")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := writeRaw(t, buf.Bytes())

	got := readLines(t, path, LineOptions{})
	if !slices.Equal(got.lines, []string{"a", "b", "c", "d"}) {
		t.Fatalf("lines = %q, want a b c d with blank lines skipped", got.lines)
	}
	if got.stats.Lines != 4 {
		t.Errorf("lines stat = %d, want 4", got.stats.Lines)
	}
}

func TestLinesCarriageReturnAcrossMemberBoundary(t *testing.T) {
	var first, second bytes.Buffer
	for _, part := range []struct {
		buf  *bytes.Buffer
		text string
	}{{&first, "a\r"}, {&second, "\nb\n"}} {
		gz := gzip.NewWriter(part.buf)
		if _, err := gz.Write([]byte(part.text)); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got := readLines(t, writeRaw(t, first.Bytes(), second.Bytes()), LineOptions{})
	if !slices.Equal(got.lines, []string{"a", "b"}) {
		t.Fatalf("lines = %q, want a b", got.lines)
	}
}

func TestLinesZeroLengthFileHasNoRecords(t *testing.T) {
	got := readLines(t, writeRaw(t), LineOptions{})
	if got.err != nil || len(got.lines) != 0 || got.stats.LostBytes != 0 {
		t.Fatalf("lines = %q stats = %+v err = %v, want an empty clean pass", got.lines, got.stats, got.err)
	}
}

func TestLinesNotGzipCountsEveryByteLost(t *testing.T) {
	path := writeRaw(t, []byte("this is not gzip\n"))
	got := readLines(t, path, LineOptions{})
	if got.err != nil {
		t.Fatalf("Err() = %v", got.err)
	}
	if len(got.lines) != 0 || got.stats.LostBytes != int64(len("this is not gzip\n")) {
		t.Fatalf("lines = %q lost = %d, want no lines and every byte lost", got.lines, got.stats.LostBytes)
	}
}

func TestReaderSalvagesTruncatedSegment(t *testing.T) {
	tail := member(t, noisy("x", 60))
	path := writeRaw(t,
		member(t, `{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"a"}}`),
		tail[:len(tail)/2],
	)
	records, stats, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 1 || stats.LostBytes == 0 {
		t.Fatalf("records = %d lost = %d, want the first record kept and the tail counted", len(records), stats.LostBytes)
	}
}

func TestLinesCountsCutFragmentInLineNumbers(t *testing.T) {
	var partial bytes.Buffer
	gz := gzip.NewWriter(&partial)
	if _, err := gz.Write([]byte("bad-partial")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Flush(); err != nil {
		t.Fatal(err)
	}
	// The member stops after the flush: no final block, no trailer.
	path := writeRaw(t, member(t, "one"), partial.Bytes(), member(t, "three"))

	lines, err := OpenLines(path, LineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer lines.Close()
	numbers := map[string]int{}
	for lines.Next() {
		numbers[string(lines.Line())] = lines.Number()
	}
	if lines.Stats().Cut != 1 {
		t.Fatalf("cut = %d, want the partial line counted", lines.Stats().Cut)
	}
	if numbers["one"] != 1 || numbers["three"] != 3 {
		t.Fatalf("line numbers = %v, want one at 1 and three at 3 after the cut line", numbers)
	}
}
