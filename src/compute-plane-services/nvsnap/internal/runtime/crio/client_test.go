// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package crio

import "testing"

// The spec of a container, exited or not, from CRI-O's verbose status.
func TestSpecOfInfo(t *testing.T) {
	info := map[string]string{"info": `{"pid":0,"runtimeSpec":{"ociVersion":"1.0.2","process":{"env":["NVIDIA_VISIBLE_DEVICES=all"]},` +
		`"linux":{"devices":[{"path":"/dev/nvidia3","type":"c","major":195,"minor":3}]}}}`}
	spec, err := specOfInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Process.Env[0] != "NVIDIA_VISIBLE_DEVICES=all" || spec.Linux.Devices[0].Path != "/dev/nvidia3" {
		t.Errorf("spec %+v", spec)
	}
	for name, bad := range map[string]map[string]string{
		"no info":  {},
		"no spec":  {"info": `{"pid":12}`},
		"not JSON": {"info": `{`},
	} {
		if _, err := specOfInfo(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
