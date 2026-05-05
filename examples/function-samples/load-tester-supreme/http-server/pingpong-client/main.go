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
	"bufio"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// headerSlice is a custom flag type for handling multiple header arguments
type headerSlice []string

func (h *headerSlice) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerSlice) Set(value string) error {
	*h = append(*h, value)
	return nil
}

func main() {
	var headers headerSlice
	serverAddr := flag.String("addr", "http://localhost:8000/ping-pong", "Server address for the ping-pong endpoint")
	flag.Var(&headers, "H", "Custom header(s) to add to the request, e.g., -H \"Authorization: Bearer token\" -H \"X-Custom: value\"")
	numPingsFlag := flag.Int("count", 5, "Number of ping-pong exchanges to perform (must be > 0 to send pings)")

	flag.Parse()

	log.Printf("Connecting to ping-pong server at %s", *serverAddr)

	// Create a pipe for the request body.
	// We will write to pipeWriter and the HTTP client will read from pipeReader.
	pipeReader, pipeWriter := io.Pipe()

	req, err := http.NewRequest(http.MethodPost, *serverAddr, pipeReader)
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	// Add custom headers from flags
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerKey := strings.TrimSpace(parts[0])
			headerValue := strings.TrimSpace(parts[1])
			if headerKey != "" && headerValue != "" {
				req.Header.Add(headerKey, headerValue)
				log.Printf("Adding header: %s: %s", headerKey, headerValue)
			} else {
				log.Printf("Warning: Skipping malformed header: %s", h)
			}
		} else {
			log.Printf("Warning: Skipping malformed header (expected Key:Value format): %s", h)
		}
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	// resp.Body will be closed by a defer further down or on error

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close() // Close immediately on non-OK status
		log.Fatalf("Server returned non-OK status: %s. Body: %s", resp.Status, string(bodyBytes))
	}

	numPings := *numPingsFlag
	log.Printf("Connected to server. Will attempt %d ping-pong exchanges.", numPings)

	// Scanner for reading server responses
	scanner := bufio.NewScanner(resp.Body)
	defer resp.Body.Close() // Ensure response body is closed when main function exits or loop breaks

	// Ensure pipeWriter is closed when this function/scope exits.
	// This signals the end of the request body stream to the server.
	defer pipeWriter.Close()

	if numPings <= 0 {
		log.Println("Ping count is 0 or negative.")
		return
	}

	for pingNum := 0; pingNum < numPings; pingNum++ {
		log.Printf("Attempting ping %d/%d...", pingNum+1, numPings)

		// Send "ping"
		message := "ping\n"
		if _, err := pipeWriter.Write([]byte(message)); err != nil {
			log.Printf("Error sending ping %d/%d: %v. Shutting down.", pingNum+1, numPings, err)
			break // Exit the for loop
		}
		log.Printf("Sent ping %d/%d.", pingNum+1, numPings)

		// Read "pong"
		if scanner.Scan() {
			receivedMessage := strings.TrimSpace(scanner.Text())
			log.Printf("Received for ping %d/%d: %s", pingNum+1, numPings, receivedMessage)
			if receivedMessage != "pong" {
				log.Printf("Error: received unexpected message for ping %d/%d: '%s'. Expected 'pong'. Shutting down.", pingNum+1, numPings, receivedMessage)
				break // Exit the for loop
			}
		} else {
			if err := scanner.Err(); err != nil {
				log.Printf("Error reading pong for ping %d/%d from server: %v. Shutting down.", pingNum+1, numPings, err)
			} else {
				log.Printf("Server closed connection (EOF while reading pong for ping %d/%d). Shutting down.", pingNum+1, numPings)
			}
			break // Exit the for loop
		}

		// If not the last ping, wait for 1 second.
		if pingNum < numPings-1 {
			log.Printf("Ping %d/%d successful. Waiting 1 second before next ping...", pingNum+1, numPings)
			time.Sleep(1 * time.Second) // Direct sleep, not interruptible within this scope by sigChan for early exit
		} else {
			log.Printf("All %d pings completed successfully.", numPings)
		}
	}

	log.Println("Ping-pong operation concluded.")
}
