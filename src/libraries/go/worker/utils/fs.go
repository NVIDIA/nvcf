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

package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zip"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/zap"
)

const (
	OneKB = 1024
	OneMB = 1024 * 1024
	OneGB = 1024 * 1024 * 1024
)

// Utility function to get the size of a directory.
func GetDirectorySize(dir string) (int64, error) {
	var size int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return size, err
	}
	return size, nil
}

func SanitizePath(path string) string {
	// Need to add the / prefix to help with sanitation - we'll remove it right after
	sanitizedPath := filepath.Clean(filepath.Join("/", path))
	if strings.HasPrefix(path, "/") {
		return sanitizedPath
	}
	return sanitizedPath[1:]
}

func CreateDirectory(directoryPath string, permissions os.FileMode) error {
	err := os.MkdirAll(directoryPath, permissions)
	if err != nil {
		formattedErr := fmt.Errorf("failed to make directory %s: %w", directoryPath, err)
		zap.L().Info(formattedErr.Error())
		return formattedErr
	}

	err = os.Chmod(directoryPath, permissions)
	if err != nil {
		formattedErr := fmt.Errorf("failed to chmod directory %s: %w", directoryPath, err)
		zap.L().Info(formattedErr.Error())
		return formattedErr
	}
	zap.L().Debug("created directory",
		zap.String("directory", directoryPath),
		zap.Any("permissions", permissions),
	)
	return nil
}

func AddFileToZip(filePath, folderPath string, zipWriter *zip.Writer) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	relPath, err := filepath.Rel(folderPath, filePath)
	if err != nil {
		return err
	}

	zipFile, err := zipWriter.Create(relPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(zipFile, file)
	return err
}

func ZipFolder(folderPath string, target io.Writer) error {
	zipWriter := zip.NewWriter(target)
	defer zipWriter.Close()

	// Walk through the folder and add files to the zip file
	return filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		return AddFileToZip(path, folderPath, zipWriter)
	})
}

// Read content from reader into buffer and output them into a channel.
func MultipartReadToBuffer(src io.Reader, dst chan<- *bytebufferpool.ByteBuffer, maxPartSize int64) (int64, error) {
	totalSize := int64(0)
	// Expects caller to close channel
	for {
		buffer := bytebufferpool.Get()
		written, err := io.CopyN(buffer, src, maxPartSize)
		totalSize += written
		if err != nil {
			// actual error occurred, clean up and abort
			if err != io.EOF {
				bytebufferpool.Put(buffer)
				return totalSize, err
			}
			// EOF
			if written > 0 {
				dst <- buffer
			} else {
				bytebufferpool.Put(buffer)
			}
			break
		}
		// no error, push the buffer and keep reading
		dst <- buffer
	}

	return totalSize, nil
}
