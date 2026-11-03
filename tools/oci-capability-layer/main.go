// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command oci-capability-layer packages an executable with Linux file capabilities.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func capabilityValue(names string) ([]byte, error) {
	var mask uint64
	for _, name := range strings.Split(names, ",") {
		switch name {
		case "CHOWN":
			mask |= 1
		default:
			return nil, fmt.Errorf("unsupported file capability %q", name)
		}
	}
	// VFS_CAP_REVISION_2 with the effective flag and permitted bits only.
	value := make([]byte, 20)
	binary.LittleEndian.PutUint32(value, 0x02000001)
	binary.LittleEndian.PutUint32(value[4:], uint32(mask))
	binary.LittleEndian.PutUint32(value[12:], uint32(mask>>32))
	return value, nil
}

func writeLayer(dst io.Writer, src io.Reader, size int64, name string, capabilities []byte) error {
	if !path.IsAbs(name) || path.Clean(name) != name || name == "/" || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("binary path must be an absolute, clean file path: %q", name)
	}
	gz := gzip.NewWriter(dst)
	tw := tar.NewWriter(gz)
	header := &tar.Header{
		Name: strings.TrimPrefix(name, "/"), Mode: 0o755, Size: size,
		Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.security.capability": string(capabilities)},
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write executable header: %w", err)
	}
	if _, err := io.Copy(tw, src); err != nil {
		return fmt.Errorf("write executable: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish tar layer: %w", err)
	}
	return gz.Close()
}

func run() error {
	input := flag.String("binary", "", "Executable to package")
	name := flag.String("path", "", "Absolute executable path in the image")
	output := flag.String("output", "", "Output tar.gz layer")
	caps := flag.String("capabilities", "", "Comma-separated Linux file capabilities (CHOWN)")
	flag.Parse()
	value, err := capabilityValue(*caps)
	if err != nil {
		return err
	}
	src, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable is not a regular file: %s", *input)
	}
	dst, err := os.Create(*output)
	if err != nil {
		return err
	}
	if err := writeLayer(dst, src, info.Size(), *name, value); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
