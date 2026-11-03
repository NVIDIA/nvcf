/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package hostlibs

import (
	"runtime"
	"strings"
	"testing"
)

func TestTripletMatchesArch(t *testing.T) {
	got := Triplet()
	want := map[string]string{"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}[runtime.GOARCH]
	if want != "" && got != want {
		t.Fatalf("Triplet() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "-linux-gnu") {
		t.Fatalf("Triplet() = %q, want a -linux-gnu suffix", got)
	}
	if Dir() != "/usr/lib/"+got || DriverDir("/run/nvidia/driver") != "/run/nvidia/driver/usr/lib/"+got {
		t.Fatalf("Dir()/DriverDir() do not use the triplet: %q %q", Dir(), DriverDir("/run/nvidia/driver"))
	}
}
