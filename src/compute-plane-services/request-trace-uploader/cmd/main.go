// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// request-trace-uploader validates and discovers Dynamo request-trace segments.
//
// Backends register themselves from an init function, so a build links only
// the backends it imports. This binary links debug, which reports what it
// read and exports nothing, and objectstore, a generic S3-compatible export
// backend with no NVIDIA-internal dependencies. That makes the binary
// runnable against a real Dynamo with no credentials and no destination, and
// able to export without any additional wiring.
//
// It links no backend that carries NVIDIA-internal dependencies. A
// distribution that needs one, such as Kratos Bulk Upload, imports it in its
// own main and reuses the packages here. Startup is runner.Run, which every
// such main calls, so distributions differ only in their backend imports and
// options.
package main

import (
	"os"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/runner"

	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/debug"
	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/objectstore"
)

func main() {
	os.Exit(runner.Run("oss"))
}
