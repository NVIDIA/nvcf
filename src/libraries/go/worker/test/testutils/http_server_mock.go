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

package testutils

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"go.uber.org/zap"

	b64 "encoding/base64"
)

func NewInferenceServer(URL string) (*httptest.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("Success!"))
		zap.L().Debug("Done writing response")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, r.Body)
	})
	mux.HandleFunc("/echo-stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("data: echo start\n\n"))
		_, _ = w.Write([]byte("data: "))
		// the HTTP/1.x server in Go closes the *http.Request.Body after the first flush of the http.ResponseWriter
		_, _ = io.Copy(w, r.Body)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: echo end\n\n"))
	})
	mux.HandleFunc("/echo-with-delay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		time.Sleep(2 * time.Second)
		_, _ = w.Write(buf)
	})
	mux.HandleFunc("/echo-with-status/{status}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		status, _ := strconv.Atoi(r.PathValue("status"))
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(status)
		_, _ = io.Copy(w, r.Body)
	})
	mux.HandleFunc("/body-counter", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
			return
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		zap.L().Info("Body counter", zap.Int64("counter", n))
	})
	mux.HandleFunc("POST /ping-pong", func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).EnableFullDuplex()
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		scanner := bufio.NewScanner(r.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "ping" {
				zap.L().Info("ping received")
				_, _ = w.Write([]byte("pong\n"))
				_ = http.NewResponseController(w).Flush()
				zap.L().Info("pong sent")
			} else {
				http.Error(w, fmt.Sprintf("unexpected line: %s", line), http.StatusBadRequest)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			http.Error(w, fmt.Sprintf("error reading request: %s", err), http.StatusInternalServerError)
			return
		}
	})

	return newHttpServer(URL, mux.ServeHTTP)
}

func NewAssetServer(URL string) (*httptest.Server, error) {
	return newHttpServer(URL, assetHandler)
}

func NewS3Server(URL string) (*httptest.Server, error) {
	return newHttpServer(URL, s3Handler)
}

func newHttpServer(URL string, handler http.HandlerFunc) (*httptest.Server, error) {
	ts := httptest.NewUnstartedServer(handler)
	if URL != "" {
		l, err := net.Listen("tcp", URL)
		if err != nil {
			return nil, err
		}
		_ = ts.Listener.Close()
		ts.Listener = l
	}
	zap.L().Info("Starting server", zap.String("url", URL))
	ts.Start()
	return ts, nil
}

func assetHandler(w http.ResponseWriter, r *http.Request) {
	zap.L().Info("Method URL", zap.String("method", r.Method), zap.String("path", r.URL.Path))

	switch r.Method {
	case http.MethodGet:
		doAssetGet(w, r)
	default:
		http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
	}
}

func doAssetGet(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/some/asset" {
		http.NotFound(w, r)
		zap.L().Error("Unknown path", zap.String("method", r.Method), zap.String("path", r.URL.Path))
		return
	}
	w.WriteHeader(http.StatusOK)
	image, _ := b64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
	_, _ = w.Write(image)
	zap.L().Debug("Done writing response")
}

func s3Handler(w http.ResponseWriter, r *http.Request) {
	zap.L().Info("Method URL", zap.String("method", r.Method), zap.String("path", r.URL.Path))
	defer r.Body.Close()

	switch r.Method {
	case http.MethodPut:
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, fmt.Sprintf("Unsupported method: %v", r.Method), http.StatusMethodNotAllowed)
	}
}
