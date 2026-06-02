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
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ------------------------------------------------------------------------

type FileInfo struct {
	Path           string
	Hash           string
	EncodedContent string
}

// ------------------------------------------------------------------------

type TestArtifacts struct {
	Name    string
	Version string
	Files   []FileInfo
}

var simpleInt8Model = TestArtifacts{
	Name:    "simple_int8",
	Version: "2",
	Files: []FileInfo{
		{
			Path:           "config.pbtxt",
			Hash:           "3fbbf8aa74482608854a31ff55e02d9700b55113c5ae862381071cc4bbccf0b5",
			EncodedContent: "bmFtZTogInNpbXBsZV9pbnQ4IgpwbGF0Zm9ybTogInRlbnNvcmZsb3dfZ3JhcGhkZWYiCm1heF9iYXRjaF9zaXplOiA4CmlucHV0IFsKICB7CiAgICBuYW1lOiAiSU5QVVQwIgogICAgZGF0YV90eXBlOiBUWVBFX0lOVDgKICAgIGRpbXM6IFsgMTYgXQogIH0sCiAgewogICAgbmFtZTogIklOUFVUMSIKICAgIGRhdGFfdHlwZTogVFlQRV9JTlQ4CiAgICBkaW1zOiBbIDE2IF0KICB9Cl0Kb3V0cHV0IFsKICB7CiAgICBuYW1lOiAiT1VUUFVUMCIKICAgIGRhdGFfdHlwZTogVFlQRV9JTlQ4CiAgICBkaW1zOiBbIDE2IF0KICB9LAogIHsKICAgIG5hbWU6ICJPVVRQVVQxIgogICAgZGF0YV90eXBlOiBUWVBFX0lOVDgKICAgIGRpbXM6IFsgMTYgXQogIH0KXQo=",
		},
		{
			Path:           "1/model.graphdef",
			Hash:           "24a40de30436c488d88c6a025ce941205327e3dd4982a9785406a5e8ec9caba6",
			EncodedContent: "CkAKBklOUFVUMBILUGxhY2Vob2xkZXIqCwoFZHR5cGUSAjAGKhwKBXNoYXBlEhM6ERILCP///////////wESAggQCkAKBklOUFVUMRILUGxhY2Vob2xkZXIqCwoFZHR5cGUSAjAGKhwKBXNoYXBlEhM6ERILCP///////////wESAggQCiMKA0FERBIDQWRkGgZJTlBVVDAaBklOUFVUMSoHCgFUEgIwBgojCgNTVUISA1N1YhoGSU5QVVQwGgZJTlBVVDEqBwoBVBICMAYKIQoHT1VUUFVUMBIISWRlbnRpdHkaA0FERCoHCgFUEgIwBgohCgdPVVRQVVQxEghJZGVudGl0eRoDU1VCKgcKAVQSAjAGIgMIhgE=",
		},
	},
}

// ------------------------------------------------------------------------

type MockArtifactServer struct {
	server *http.Server
}

// ------------------------------------------------------------------------

func artifactsHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.PathValue("filepath")

	var encodedContent string
	for _, file := range simpleInt8Model.Files {
		if file.Path == filePath {
			encodedContent = file.EncodedContent
		}
	}

	if encodedContent == "" {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "failed to find file: %s", filePath)
		return
	}

	content, err := base64.StdEncoding.DecodeString(encodedContent)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to decode content: %s", err)
		return
	}

	w.Header().Set("Content-Type", "binary/octet-stream")

	rangeValue := r.Header.Get("Range")
	if rangeValue != "" {
		start, end, err := parseRange(rangeValue)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if end > int64(len(content)) {
			end = int64(len(content)) - 1
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
		return
	}
	_, _ = w.Write(content)
}

func parseRange(rangeHeader string) (int64, int64, error) {
	rangeParts := strings.Split(rangeHeader, "=")
	if len(rangeParts) != 2 {
		return 0, 0, fmt.Errorf("bad range request")
	}

	rangeSpec := strings.Split(rangeParts[1], "-")
	if len(rangeSpec) != 2 {
		return 0, 0, fmt.Errorf("bad range request")
	}

	start, err := strconv.ParseInt(rangeSpec[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad range request")
	}

	var end int64
	if rangeSpec[1] == "" {
		end = math.MaxInt64
	} else {
		end, err = strconv.ParseInt(rangeSpec[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bad range request")
		}
	}

	if start > end {
		return 0, 0, fmt.Errorf("bad range request")
	}
	return start, end, nil
}

// ------------------------------------------------------------------------

func (s *MockArtifactServer) Start(addr string) error {
	server := &http.Server{
		Addr: addr,
	}

	httpserver := http.NewServeMux()
	httpserver.HandleFunc("/files/{filepath...}", artifactsHandler)
	server.Handler = httpserver
	s.server = server

	zap.L().Debug("Starting mock artifact server...", zap.String("address", addr))
	go func() {
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			zap.S().Panic("failed to start mock artifact server: %s", err)
		}
	}()

	return nil
}

// ------------------------------------------------------------------------

func (s *MockArtifactServer) Close(ctx context.Context) {
	cancelCtx, cancel := context.WithTimeout(ctx, 5)
	defer cancel()

	zap.L().Debug("Shutting down mock artifact server...")
	err := s.server.Shutdown(cancelCtx)
	if err != nil {
		zap.L().Error("Failed to terminate mock artifact server", zap.Error(err))
		return
	}
	zap.L().Debug("Successfully terminated mock artifact server")
}

// ------------------------------------------------------------------------

func ValidateDownloadedArtifacts(artifactPath string) error {
	for _, file := range simpleInt8Model.Files {
		filePath := filepath.Join(artifactPath, simpleInt8Model.Name, file.Path)
		if _, err := os.Stat(filePath); err != nil {
			return fmt.Errorf("artifact %s not found", filePath)
		}
		if _, err := os.Stat(filePath + ".tmp"); err == nil {
			return fmt.Errorf("artifact %s tmp file not cleaned up", filePath)
		}

		fileHash, err := GetFileHash(filePath)
		if err != nil {
			return err
		}

		if fileHash != file.Hash {
			return fmt.Errorf("get file hash: %s, want: %s", fileHash, file.Hash)
		}
	}

	return nil
}
