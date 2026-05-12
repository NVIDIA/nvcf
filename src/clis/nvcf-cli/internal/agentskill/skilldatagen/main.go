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

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const deterministicGeneratedAt = "1970-01-01T00:00:00Z"

var deterministicZipTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

var allowedSkillDirs = map[string]struct{}{
	"nvcf-self-managed-cli":          {},
	"nvcf-self-managed-installation": {},
}

type manifestJSON struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	TotalFiles    int            `json:"totalFiles"`
	TotalBytes    int            `json:"totalBytes"`
	Files         []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

type skillFile struct {
	Path string
	Body []byte
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("skilldatagen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	out := flags.String("out", "", "generated Go source output path")
	stripPrefix := flags.String("strip-prefix", "ai-tooling/user/skills", "source path prefix to strip from input files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	inputs := flags.Args()
	if len(inputs) == 0 {
		return errors.New("at least one source skill file is required")
	}

	files, err := collectSkillFiles(*stripPrefix, inputs)
	if err != nil {
		return err
	}
	zipBytes, _, err := buildSkillZip(files)
	if err != nil {
		return err
	}
	source, err := renderGoSource(zipBytes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*out, source, 0o644)
}

func collectSkillFiles(stripPrefix string, inputPaths []string) ([]skillFile, error) {
	files := make([]skillFile, 0, len(inputPaths))
	for _, inputPath := range inputPaths {
		rel, err := relativeSkillPath(stripPrefix, inputPath)
		if err != nil {
			return nil, err
		}
		if err := validateSkillPath(rel); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(inputPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", inputPath, err)
		}
		files = append(files, skillFile{Path: rel, Body: body})
	}
	return files, nil
}

func relativeSkillPath(stripPrefix, inputPath string) (string, error) {
	prefix := strings.Trim(filepath.ToSlash(stripPrefix), "/")
	input := filepath.ToSlash(inputPath)
	if strings.HasPrefix(input, prefix+"/") {
		return strings.TrimPrefix(input, prefix+"/"), nil
	}
	marker := "/" + prefix + "/"
	if idx := strings.Index(input, marker); idx >= 0 {
		return input[idx+len(marker):], nil
	}
	return "", fmt.Errorf("%s does not contain source prefix %s", inputPath, stripPrefix)
}

func validateSkillPath(rel string) error {
	if rel == "" {
		return errors.New("empty skill path")
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return fmt.Errorf("invalid skill path %q", rel)
	}
	clean := path.Clean(rel)
	if clean != rel || clean == "." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid skill path %q", rel)
	}
	parts := strings.Split(rel, "/")
	if _, ok := allowedSkillDirs[parts[0]]; !ok {
		return fmt.Errorf("unexpected skill directory %q", parts[0])
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
			return fmt.Errorf("invalid skill path segment %q in %q", part, rel)
		}
	}
	return nil
}

func buildSkillZip(files []skillFile) ([]byte, manifestJSON, error) {
	files = append([]skillFile(nil), files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	manifest := manifestJSON{
		SchemaVersion: 1,
		GeneratedAt:   deterministicGeneratedAt,
		TotalFiles:    len(files),
		Files:         make([]manifestFile, 0, len(files)),
	}
	for _, file := range files {
		if err := validateSkillPath(file.Path); err != nil {
			return nil, manifestJSON{}, err
		}
		sum := sha256.Sum256(file.Body)
		manifest.TotalBytes += len(file.Body)
		manifest.Files = append(manifest.Files, manifestFile{
			Path:   file.Path,
			SHA256: hex.EncodeToString(sum[:]),
			Size:   len(file.Body),
		})
	}

	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, manifestJSON{}, err
	}
	manifestBody = append(manifestBody, '\n')

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeZipFile(zw, "data/manifest.json", manifestBody); err != nil {
		return nil, manifestJSON{}, err
	}
	for _, file := range files {
		if err := writeZipFile(zw, "data/"+file.Path, file.Body); err != nil {
			return nil, manifestJSON{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, manifestJSON{}, err
	}
	return buf.Bytes(), manifest, nil
}

func writeZipFile(zw *zip.Writer, name string, body []byte) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Store,
	}
	header.SetModTime(deterministicZipTime)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func renderGoSource(zipBytes []byte) ([]byte, error) {
	encoded := base64.StdEncoding.EncodeToString(zipBytes)
	var b strings.Builder
	b.WriteString(`// Code generated by skilldatagen; DO NOT EDIT.

package agentskill

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"sync"
)

const skillDataZipBase64 = ""`)
	for _, chunk := range chunks(encoded, 76) {
		fmt.Fprintf(&b, " +\n\t%q", chunk)
	}
	b.WriteString(`

var (
	skillDataOnce sync.Once
	skillDataFS   fs.FS
	skillDataErr  error
)

func FS() fs.FS {
	skillDataOnce.Do(func() {
		body, err := base64.StdEncoding.DecodeString(skillDataZipBase64)
		if err != nil {
			skillDataErr = fmt.Errorf("decode embedded skill data: %w", err)
			return
		}
		reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			skillDataErr = fmt.Errorf("open embedded skill data: %w", err)
			return
		}
		skillDataFS = reader
	})
	if skillDataErr != nil {
		return errFS{err: skillDataErr}
	}
	return skillDataFS
}

type errFS struct {
	err error
}

func (e errFS) Open(string) (fs.File, error) {
	return nil, e.err
}
`)
	return format.Source([]byte(b.String()))
}

func chunks(s string, size int) []string {
	if size <= 0 || len(s) <= size {
		return []string{s}
	}
	out := make([]string, 0, (len(s)+size-1)/size)
	for len(s) > size {
		out = append(out, s[:size])
		s = s[size:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
