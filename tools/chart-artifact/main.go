// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type identity struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type artifact struct {
	identity
	files map[string][sha256.Size]byte
}

func parseIdentity(data []byte) (identity, error) {
	var id identity
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&id); err != nil {
		return id, fmt.Errorf("parse Chart.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return id, errors.New("Chart.yaml must contain exactly one YAML document")
	}
	if id.Name == "" || id.Version == "" || strings.ContainsAny(id.Name+id.Version, "/\\\r\n\t ") {
		return id, errors.New("Chart.yaml must contain a nonempty chart name and version without paths or whitespace")
	}
	return id, nil
}

func readArtifact(filename string, expected identity) (artifact, error) {
	a := artifact{files: make(map[string][sha256.Size]byte)}
	f, err := os.Open(filename)
	if err != nil {
		return a, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return a, fmt.Errorf("read gzip: %w", err)
	}
	defer gz.Close()
	// Include tar headers and trailing padding in the decompression budget.
	stream := &io.LimitedReader{R: gz, N: 640<<20 + 1}
	tr := tar.NewReader(stream)
	seen := make(map[string]bool)
	var root string
	var metadata []byte
	var total int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return a, fmt.Errorf("read tar: %w", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "." || name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || strings.HasPrefix(name, "../") {
			return a, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if seen[name] {
			return a, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = true
		if len(seen) > 100000 {
			return a, errors.New("chart archive exceeds 100000 entries")
		}
		entryRoot, _, _ := strings.Cut(name, "/")
		if root == "" {
			root = entryRoot
		} else if root != entryRoot {
			return a, errors.New("chart archive must have exactly one root directory")
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return a, fmt.Errorf("unsupported archive entry %q (type %d); only files and directories are allowed", name, header.Typeflag)
		}
		if !strings.Contains(name, "/") {
			return a, fmt.Errorf("file %q is outside the chart root", name)
		}
		total += header.Size
		if total > 512<<20 {
			return a, errors.New("chart archive exceeds 512 MiB of uncompressed file content")
		}
		hash := sha256.New()
		var output io.Writer = hash
		var chart bytes.Buffer
		if name == root+"/Chart.yaml" {
			if header.Size > 1<<20 {
				return a, errors.New("Chart.yaml exceeds 1 MiB")
			}
			output = io.MultiWriter(hash, &chart)
		}
		if _, err := io.Copy(output, tr); err != nil {
			return a, fmt.Errorf("read %s: %w", name, err)
		}
		if name == root+"/Chart.yaml" {
			metadata = chart.Bytes()
		}
		a.files[name] = [sha256.Size]byte(hash.Sum(nil))
	}
	// tar.Reader stops at the end marker, before gzip verifies its checksum.
	padding := make([]byte, 32<<10)
	for {
		n, err := stream.Read(padding)
		if stream.N == 0 {
			return a, errors.New("chart archive exceeds 640 MiB including headers and padding")
		}
		for _, b := range padding[:n] {
			if b != 0 {
				return a, errors.New("chart archive contains nonpadding data after its tar end marker")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return a, fmt.Errorf("finish gzip: %w", err)
		}
	}
	if metadata == nil {
		return a, errors.New("chart archive has no root Chart.yaml")
	}
	a.identity, err = parseIdentity(metadata)
	if err != nil {
		return a, err
	}
	if root != a.Name {
		return a, fmt.Errorf("archive root %q does not match chart name %q", root, a.Name)
	}
	if a.identity != expected {
		return a, fmt.Errorf("chart identity is %s:%s, expected %s:%s", a.Name, a.Version, expected.Name, expected.Version)
	}
	return a, nil
}

func (a artifact) digest() string {
	names := make([]string, 0, len(a.files))
	for name := range a.files {
		names = append(names, name)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, name := range names {
		// Length-prefix paths so file boundaries cannot make ambiguous input.
		fmt.Fprintf(h, "%d:%s%x\n", len(name), name, a.files[name])
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func compare(source, destination artifact) error {
	names := make([]string, 0, len(source.files)+len(destination.files))
	for name := range source.files {
		names = append(names, name)
	}
	for name := range destination.files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		src, sourceExists := source.files[name]
		dst, destinationExists := destination.files[name]
		if !sourceExists || !destinationExists || src != dst {
			return fmt.Errorf("chart content differs at %s; publish a new version instead of replacing existing content", name)
		}
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 && args[0] == "discover" {
		for _, root := range args[1:] {
			if err := filepath.WalkDir(root, func(dir string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					return nil
				}
				if dir != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "testdata") {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err == nil {
					fmt.Fprintln(stdout, dir)
					// Dependencies are part of their parent's package.
					return filepath.SkipDir
				} else if !os.IsNotExist(err) {
					return err
				}
				return nil
			}); err != nil {
				return fmt.Errorf("discover charts under %s: %w", root, err)
			}
		}
		return nil
	}
	if len(args) == 0 || (args[0] != "validate" && args[0] != "compare") {
		return errors.New("usage: chart-artifact discover ROOT... | validate|compare (--metadata Chart.yaml | --name NAME --version VERSION) SOURCE.tgz [DESTINATION.tgz]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	metadata := flags.String("metadata", "", "source Chart.yaml containing the expected name and version")
	name := flags.String("name", "", "expected chart name")
	version := flags.String("version", "", "expected chart version")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	count := 1
	if args[0] == "compare" {
		count = 2
	}
	if flags.NArg() != count {
		return fmt.Errorf("%s requires %d archive path(s)", args[0], count)
	}
	expected := identity{*name, *version}
	if *metadata != "" {
		if *name != "" || *version != "" {
			return errors.New("use --metadata or --name with --version, not both")
		}
		data, err := os.ReadFile(*metadata)
		if err != nil {
			return fmt.Errorf("read source metadata: %w", err)
		}
		expected, err = parseIdentity(data)
		if err != nil {
			return err
		}
	} else if *name == "" || *version == "" {
		return errors.New("expected identity is required: use --metadata or both --name and --version")
	}
	source, err := readArtifact(flags.Arg(0), expected)
	if err != nil {
		return fmt.Errorf("source %s: %w", flags.Arg(0), err)
	}
	if count == 2 {
		destination, err := readArtifact(flags.Arg(1), expected)
		if err != nil {
			return fmt.Errorf("destination %s: %w", flags.Arg(1), err)
		}
		if err := compare(source, destination); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "%s %s:%s %s\n", args[0], source.Name, source.Version, source.digest())
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "chart-artifact:", err)
		os.Exit(1)
	}
}
