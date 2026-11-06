// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// readDir lists the spool. A var so a test can change the spool between the
// listing and the per-file stat.
var readDir = os.ReadDir

// Scan is one listing of the spool.
type Scan struct {
	// Closed holds every segment a writer has rolled past. Within a prefix
	// the order is numeric index ascending, which is write order; across
	// prefixes the queues are merged oldest first, so one family's backlog
	// does not hold back the other.
	Closed []Segment
	// Active holds the highest indexed segment of each prefix, or every
	// file at that index when names share it. The writer may still append
	// to it, so it is never handed off or deleted.
	Active []Segment
	// Skipped counts entries that look like segments but are not usable.
	Skipped Skipped
}

// Skipped counts spool entries that were not treated as segments.
type Skipped struct {
	// OddNames counts names that start with a configured prefix and a dot
	// but do not match <prefix>.<index>.jsonl.gz with an index of at least
	// six digits that fits an int.
	OddNames int
	// NonRegular counts entries with a segment name that are not regular
	// files, such as directories and symbolic links.
	NonRegular int
}

// ValidatePrefixes rejects a prefix list that cannot be scanned safely: an
// empty list, an empty prefix or one with a path separator, a duplicate, or a
// prefix that is a dotted extension of another, such as "request-trace" and
// "request-trace.x". The anchored name pattern cannot match both families,
// but an extension still means the writer configuration is wrong.
func ValidatePrefixes(prefixes []string) error {
	if len(prefixes) == 0 {
		return errors.New("at least one segment prefix is required")
	}
	for i, prefix := range prefixes {
		if strings.TrimSpace(prefix) == "" {
			return errors.New("segment prefix must not be empty")
		}
		if strings.ContainsAny(prefix, `/\`) {
			return fmt.Errorf("segment prefix %q must not contain a path separator", prefix)
		}
		for _, other := range prefixes[:i] {
			if prefix == other {
				return fmt.Errorf("segment prefix %q is listed twice", prefix)
			}
			if strings.HasPrefix(prefix, other+".") || strings.HasPrefix(other, prefix+".") {
				return fmt.Errorf("segment prefixes %q and %q overlap", other, prefix)
			}
		}
	}
	return nil
}

type family struct {
	prefix  string
	pattern *regexp.Regexp
}

func families(prefixes []string) []family {
	out := make([]family, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, family{
			prefix:  prefix,
			pattern: regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `\.(\d{6,})\.jsonl\.gz$`),
		})
	}
	return out
}

// ScanDir lists the top level of directory once and classifies segments for
// every prefix. Subdirectories are never entered.
func ScanDir(directory string, prefixes []string) (Scan, error) {
	if err := ValidatePrefixes(prefixes); err != nil {
		return Scan{}, err
	}
	entries, err := readDir(directory)
	if err != nil {
		return Scan{}, fmt.Errorf("read request trace directory: %w", err)
	}
	fams := families(prefixes)
	byPrefix := make([][]Segment, len(fams))
	var skipped Skipped
	for _, entry := range entries {
		name := entry.Name()
		for f, fam := range fams {
			if !strings.HasPrefix(name, fam.prefix+".") {
				continue
			}
			matches := fam.pattern.FindStringSubmatch(name)
			if matches == nil {
				skipped.OddNames++
				continue
			}
			index, err := strconv.Atoi(matches[1])
			if err != nil {
				skipped.OddNames++
				continue
			}
			if !entry.Type().IsRegular() {
				skipped.NonRegular++
				continue
			}
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				slog.Debug("request trace segment vanished during scan", "prefix", fam.prefix, "segment", index)
				continue
			}
			if err != nil {
				return Scan{}, fmt.Errorf("stat request trace segment %q: %w", name, err)
			}
			byPrefix[f] = append(byPrefix[f], Segment{
				Path:    filepath.Join(directory, name),
				Prefix:  fam.prefix,
				Index:   index,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}

	scan := Scan{Skipped: skipped}
	queues := make([][]Segment, 0, len(byPrefix))
	for _, segments := range byPrefix {
		if len(segments) == 0 {
			continue
		}
		sort.SliceStable(segments, func(i, j int) bool { return segments[i].Index < segments[j].Index })
		// Every file at the highest index is active: two names can carry the
		// same number, such as 000001 and 0000001, and neither is proven
		// closed until a higher index exists.
		first := len(segments) - 1
		for first > 0 && segments[first-1].Index == segments[first].Index {
			first--
		}
		scan.Active = append(scan.Active, segments[first:]...)
		if first > 0 {
			queues = append(queues, segments[:first])
		}
	}
	scan.Closed = mergeOldestFirst(queues)
	return scan, nil
}

// mergeOldestFirst merges per-prefix queues by the age of each queue's head,
// keeping index order inside every queue. Ties keep prefix order.
func mergeOldestFirst(queues [][]Segment) []Segment {
	total := 0
	for _, queue := range queues {
		total += len(queue)
	}
	merged := make([]Segment, 0, total)
	for len(merged) < total {
		pick := -1
		for q, queue := range queues {
			if len(queue) == 0 {
				continue
			}
			if pick < 0 || queue[0].ModTime.Before(queues[pick][0].ModTime) {
				pick = q
			}
		}
		merged = append(merged, queues[pick][0])
		queues[pick] = queues[pick][1:]
	}
	return merged
}
