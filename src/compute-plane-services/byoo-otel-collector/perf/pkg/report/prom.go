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

package report

import (
	"strconv"
	"strings"
)

// Sample is a single parsed Prometheus time series: a metric name, its label
// set, and its value.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Samples is a parsed Prometheus exposition scrape.
type Samples []Sample

// Parse parses Prometheus text exposition format into samples. It is a
// deliberately small parser covering the counter/gauge lines the collector and
// sink emit ("name{label="v",...} 123.4" and "name 123"); it ignores HELP/TYPE
// comments, timestamps, and unparseable lines rather than failing, so a scrape
// is best-effort.
func Parse(text string) Samples {
	var out Samples
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, rest, ok := splitNameLabels(line)
		if !ok {
			continue
		}
		// rest is "<value>" or "<value> <timestamp>"; take the first field.
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		out = append(out, Sample{Name: name, Labels: labels, Value: v})
	}
	return out
}

// splitNameLabels splits a metric line into its name, labels, and the remainder
// (value [timestamp]).
func splitNameLabels(line string) (name string, labels map[string]string, rest string, ok bool) {
	brace := strings.IndexByte(line, '{')
	if brace < 0 {
		// No labels: "name value".
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			return "", nil, "", false
		}
		return line[:sp], map[string]string{}, line[sp+1:], true
	}
	name = line[:brace]
	close := strings.IndexByte(line[brace:], '}')
	if close < 0 {
		return "", nil, "", false
	}
	close += brace
	labels = parseLabels(line[brace+1 : close])
	rest = strings.TrimSpace(line[close+1:])
	return name, labels, rest, true
}

// parseLabels parses `k1="v1",k2="v2"` into a map. It is tolerant of spaces and
// missing quotes.
func parseLabels(s string) map[string]string {
	labels := map[string]string{}
	for _, pair := range splitTopLevelCommas(s) {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])
		val = strings.Trim(val, `"`)
		labels[key] = val
	}
	return labels
}

// splitTopLevelCommas splits on commas that are not inside quotes.
func splitTopLevelCommas(s string) []string {
	var parts []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			b.WriteByte(c)
		case c == ',' && !inQuote:
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

// Sum returns the summed value of every sample whose name equals one of the
// candidate names, using the first candidate that matches any sample. Metric
// name suffixes vary across collector versions (e.g. a "_total" suffix), so
// callers pass the plausible names in priority order.
func (s Samples) Sum(candidates ...string) (value float64, found bool) {
	for _, name := range candidates {
		var sum float64
		matched := false
		for _, smp := range s {
			if smp.Name == name {
				sum += smp.Value
				matched = true
			}
		}
		if matched {
			return sum, true
		}
	}
	return 0, false
}

// Latest returns the maximum value across samples matching one of the candidate
// names (useful for gauges like queue size or memory). Uses the first candidate
// that matches.
func (s Samples) Latest(candidates ...string) (value float64, found bool) {
	for _, name := range candidates {
		var max float64
		matched := false
		for _, smp := range s {
			if smp.Name == name {
				if !matched || smp.Value > max {
					max = smp.Value
				}
				matched = true
			}
		}
		if matched {
			return max, true
		}
	}
	return 0, false
}
