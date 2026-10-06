// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import "testing"

// Upstream CRIU renamed --compress-region to --compress-block; the dump
// uses whichever the bundled binary advertises and the older name when the
// probe says nothing.
func TestCriuCompressBlockFlag(t *testing.T) {
	cases := map[string]string{
		"Usage: criu ...\n  --compress-block size  enable memory page compression": "--compress-block",
		"Usage: criu ...\n  --compress-region size enable memory page compression": "--compress-region",
		"": "--compress-region",
	}
	for help, want := range cases {
		if got := criuCompressBlockFlag(help); got != want {
			t.Errorf("help %q: got %s want %s", help, got, want)
		}
	}
}
