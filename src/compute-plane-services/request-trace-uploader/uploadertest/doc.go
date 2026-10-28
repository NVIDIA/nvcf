// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package uploadertest is the shared test harness for the request-trace
// uploader and for modules that build on it. Only tests import it.
//
// It has two parts:
//
//   - Fixtures: line builders for Dynamo v1.4.0 request trace records, a
//     SegmentBuilder that writes one gzip member per flush (including cut-off
//     members), and a Spool that names and numbers segments the way Dynamo
//     does.
//   - Crash helper: RunCrash re-runs one test in a child process and kills it
//     with SIGKILL at a named crash point. CrashPoint does nothing outside a
//     test binary, so crash points are inactive in any other binary.
//
// The fake asynchronous backend lives in package backend/backendtest.
//
// All fixture content is synthetic. Never add production payloads.
package uploadertest
