/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package modelvolume

import "testing"

func TestSeedToken(t *testing.T) {
	tok := SeedToken("secret", "sr-fn", "cache://abc/0")
	if !SeedTokenValid("secret", "sr-fn", "cache://abc/0", tok) {
		t.Fatal("a token must validate for the namespace and key it was issued for")
	}
	for name, bad := range map[string][3]string{
		"other namespace": {"secret", "sr-other", "cache://abc/0"},
		"other key":       {"secret", "sr-fn", "cache://abc/1"},
		"other secret":    {"rotated", "sr-fn", "cache://abc/0"},
	} {
		if SeedTokenValid(bad[0], bad[1], bad[2], tok) {
			t.Errorf("%s: token must not validate", name)
		}
	}
	if SeedTokenValid("secret", "sr-fn", "cache://abc/0", "") {
		t.Error("an empty token never validates")
	}
	if SeedToken("", "sr-fn", "cache://abc/0") == "" || SeedToken("", "sr-fn", "cache://abc/0") == SeedToken("", "sr-fn", "cache://abc/1") {
		t.Error("without a secret the token still binds namespace and key")
	}
}
