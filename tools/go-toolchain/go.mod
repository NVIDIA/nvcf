// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// The Go toolchain version for this repository, declared once.
//
// This module has no source and is never built. It exists so that one file
// declares the toolchain and every consumer derives from it rather than
// repeating a literal:
//
//   - Bazel reads it via go_sdk.from_file in the root MODULE.bazel.
//   - GitHub Actions read it via setup-go's go-version-file.
//   - tools/ci/check-go-version asserts everything else agrees with it.
//
// rules_go's from_file requires a file named exactly go.mod, which is why this
// is a module rather than a plain .go-version file.
//
// To change the toolchain, edit the toolchain line below and nothing else, then
// run tools/ci/check-go-version to find anything that has drifted.

module github.com/NVIDIA/nvcf/tools/go-toolchain

go 1.26.5

toolchain go1.26.5
