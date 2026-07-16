/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package stats

import (
	"fmt"
	"strings"
	"sync"
)

// FailedKey records details of a failed key
type FailedKey struct {
	Iteration int
	Kid       string
	DLength   int
	XLength   int
	YLength   int
	Error     string
}

// Stats collects test results
type Stats struct {
	mu sync.Mutex

	Total      int
	Passed     int
	Failed     int
	ParseError int

	DLengthDist map[int]int
	FailedKeys  []FailedKey
}

// New creates a new Stats collector
func New() *Stats {
	return &Stats{
		DLengthDist: make(map[int]int),
		FailedKeys:  make([]FailedKey, 0),
	}
}

// Record records the result of a single test iteration
func (s *Stats) Record(iteration int, kid string, dLen, xLen, yLen int, valid bool, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Total++
	s.DLengthDist[dLen]++

	if errMsg != "" {
		s.ParseError++
		s.FailedKeys = append(s.FailedKeys, FailedKey{
			Iteration: iteration,
			Kid:       kid,
			DLength:   dLen,
			XLength:   xLen,
			YLength:   yLen,
			Error:     errMsg,
		})
	} else if valid {
		s.Passed++
	} else {
		s.Failed++
		s.FailedKeys = append(s.FailedKeys, FailedKey{
			Iteration: iteration,
			Kid:       kid,
			DLength:   dLen,
			XLength:   xLen,
			YLength:   yLen,
			Error:     "signature verification failed",
		})
	}
}

// PrintSummary outputs the test results
func (s *Stats) PrintSummary(codeVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	width := 65

	fmt.Println()
	fmt.Println(strings.Repeat("═", width))
	fmt.Println("           JWT SIGNING KEY TEST HARNESS - RESULTS")
	fmt.Println(strings.Repeat("═", width))
	fmt.Println()

	fmt.Printf("  Code Version:  %s\n", codeVersion)
	fmt.Printf("  Iterations:    %d\n", s.Total)
	fmt.Println()

	fmt.Println(strings.Repeat("─", width))
	fmt.Println("                         RESULTS")
	fmt.Println(strings.Repeat("─", width))

	passRate := float64(s.Passed) / float64(s.Total) * 100
	failRate := float64(s.Failed) / float64(s.Total) * 100
	parseRate := float64(s.ParseError) / float64(s.Total) * 100

	fmt.Printf("  Passed:       %5d  (%6.2f%%)\n", s.Passed, passRate)
	fmt.Printf("  Failed:       %5d  (%6.2f%%)  ← Signature verification failed\n", s.Failed, failRate)
	fmt.Printf("  Parse Errors: %5d  (%6.2f%%)\n", s.ParseError, parseRate)
	fmt.Println()

	fmt.Println(strings.Repeat("─", width))
	fmt.Println("                   'd' LENGTH DISTRIBUTION")
	fmt.Println(strings.Repeat("─", width))

	// Show lengths from 40 to 44
	for l := 40; l <= 44; l++ {
		count := s.DLengthDist[l]
		pct := float64(count) / float64(s.Total) * 100

		indicator := ""
		if l < 43 && count > 0 {
			indicator = "  ← CORRUPTED"
		} else if l == 43 {
			indicator = "  ← VALID"
		}

		fmt.Printf("  %d chars:  %5d  (%6.2f%%)%s\n", l, count, pct, indicator)
	}
	fmt.Println()

	// Show failed samples (up to 10)
	if len(s.FailedKeys) > 0 {
		fmt.Println(strings.Repeat("─", width))
		fmt.Println("                     FAILED SAMPLES")
		fmt.Println(strings.Repeat("─", width))

		limit := 10
		if len(s.FailedKeys) < limit {
			limit = len(s.FailedKeys)
		}

		for i := 0; i < limit; i++ {
			f := s.FailedKeys[i]
			kidDisplay := f.Kid
			if len(kidDisplay) > 18 {
				kidDisplay = kidDisplay[:18] + "..."
			}
			fmt.Printf("  #%-4d  d=%d chars  kid=%s\n", f.Iteration, f.DLength, kidDisplay)
		}

		if len(s.FailedKeys) > limit {
			fmt.Printf("  ... and %d more\n", len(s.FailedKeys)-limit)
		}
		fmt.Println()
	}

	// Conclusion
	fmt.Println(strings.Repeat("─", width))
	fmt.Println("                       CONCLUSION")
	fmt.Println(strings.Repeat("─", width))

	if s.Failed > 0 || s.ParseError > 0 {
		expectedRate := 0.39
		observedRate := float64(s.Failed+s.ParseError) / float64(s.Total) * 100

		fmt.Println("  Bug Confirmed:    YES")
		fmt.Printf("  Expected Rate:    ~%.2f%%\n", expectedRate)
		fmt.Printf("  Observed Rate:    %.2f%%\n", observedRate)

		if s.Total >= 100 && (s.Failed+s.ParseError) >= 1 {
			fmt.Println("  Statistically Significant: ✓")
		}
	} else {
		fmt.Println("  Bug Confirmed:    NO")
		fmt.Println("  All keys valid:   ✓")
	}

	fmt.Println()
	fmt.Println(strings.Repeat("═", width))
}

