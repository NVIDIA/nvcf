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

package clusterdump

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"time"
)

// RenderArchive writes the deep dump as a gzipped tar to w. The layout is
// identical to RenderDirectory (both go through writeDumpTree).
func RenderArchive(w io.Writer, d Dump) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	modTime := d.CollectedAt
	if modTime.IsZero() {
		modTime = time.Unix(0, 0)
	}

	writeErr := writeDumpTree(d, func(rel string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name:    rel,
			Mode:    0o600,
			Size:    int64(len(body)),
			ModTime: modTime,
		}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	})
	if writeErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return writeErr
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}
