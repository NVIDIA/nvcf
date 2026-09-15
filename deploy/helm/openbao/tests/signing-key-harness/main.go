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

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"signing-key-harness/jwt"
	"signing-key-harness/keygen"
	"signing-key-harness/stats"
)

func main() {
	// CLI flags
	iterations := flag.Int("n", 1000, "Number of iterations to run")
	scriptPath := flag.String("script", "", "Path to encryption_setup.sh (required)")
	codeVersion := flag.String("version", "unknown", "Code version label (e.g., 'buggy' or 'fixed')")
	workers := flag.Int("workers", runtime.NumCPU(), "Number of parallel workers")
	flag.Parse()

	if *iterations <= 0 {
		fmt.Fprintln(os.Stderr, "Error: --n must be a positive integer")
		os.Exit(1)
	}

	if *scriptPath == "" {
		fmt.Println("Error: --script flag is required")
		fmt.Println("Usage: signing-key-harness --script /path/to/encryption_setup.sh")
		os.Exit(1)
	}

	// Resolve absolute path
	absPath, err := filepath.Abs(*scriptPath)
	if err != nil {
		fmt.Printf("Error resolving script path: %v\n", err)
		os.Exit(1)
	}

	// Verify script exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		fmt.Printf("Error: script not found: %s\n", absPath)
		os.Exit(1)
	}

	// Print header
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║          JWT Signing Key Test Harness (Go)                   ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Script:      %-47s ║\n", truncateString(filepath.Base(absPath), 47))
	fmt.Printf("║  Version:     %-47s ║\n", truncateString(*codeVersion, 47))
	fmt.Printf("║  Iterations:  %-47d ║\n", *iterations)
	fmt.Printf("║  Workers:     %-47d ║\n", *workers)
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Create generator and stats
	gen := keygen.NewGenerator(absPath)
	s := stats.New()

	// Progress tracking
	var completed int64
	total := int64(*iterations)

	// Progress display goroutine
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				current := atomic.LoadInt64(&completed)
				pct := float64(current) / float64(total) * 100
				bar := progressBar(int(pct), 40)
				fmt.Printf("\rRunning... [%s] %d/%d (%.1f%%)", bar, current, total, pct)
			}
		}
	}()

	// Run tests
	startTime := time.Now()

	// Use worker pool for parallel execution
	var wg sync.WaitGroup
	workChan := make(chan int, *iterations)

	// Start workers
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := range workChan {
				runSingleTest(gen, s, iteration)
				atomic.AddInt64(&completed, 1)
			}
		}()
	}

	// Send work
	for i := 0; i < *iterations; i++ {
		workChan <- i
	}
	close(workChan)

	// Wait for completion
	wg.Wait()
	close(done)

	elapsed := time.Since(startTime)

	// Clear progress line
	fmt.Printf("\rRunning... [%s] %d/%d (100.0%%)  \n", progressBar(100, 40), total, total)
	fmt.Printf("\nCompleted in %v (%.1f keys/sec)\n", elapsed.Round(time.Millisecond), float64(*iterations)/elapsed.Seconds())

	// Print results
	s.PrintSummary(*codeVersion)

	// Exit with error code if failures detected
	if s.Failed > 0 || s.ParseError > 0 {
		os.Exit(1)
	}
}

// runSingleTest executes one iteration of the test
func runSingleTest(gen *keygen.Generator, s *stats.Stats, iteration int) {
	result, err := gen.Generate()
	if err != nil {
		s.Record(iteration, "unknown", 0, 0, 0, false, err.Error())
		return
	}

	if result.ParseError != nil {
		s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, result.ParseError.Error())
		return
	}

	// Convert to ECDSA key
	privateKey, err := result.JWK.ToECDSAPrivateKey()
	if err != nil {
		s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
		return
	}

	// Sign JWT
	token, err := jwt.Sign(privateKey, result.JWK.Kid)
	if err != nil {
		s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
		return
	}

	// Verify JWT
	valid, err := jwt.Verify(token, &privateKey.PublicKey)
	if err != nil {
		s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
		return
	}

	s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, valid, "")
}

// progressBar creates an ASCII progress bar
func progressBar(percent, width int) string {
	filled := percent * width / 100
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	return bar
}

// truncateString truncates a string to maxLen
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

