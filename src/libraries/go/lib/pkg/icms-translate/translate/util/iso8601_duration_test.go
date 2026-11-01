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

package translateutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseISO8601Duration(t *testing.T) {
	cases := []struct {
		from string
		want time.Duration
	}{
		{"P1Y", year},
		{"P1M", month},
		{"P2M", 2 * month},
		{"P1W", week},
		{"P1D", day},
		{"PT1H", 1 * time.Hour},
		{"PT1M", 1 * time.Minute},
		{"PT1S", 1 * time.Second},
		{"P10Y5M8DT5H10M6S", 10*year + 5*month + 8*day + 5*time.Hour + 10*time.Minute + 6*time.Second},
		// {"P10Y5M8DT5H10M6S", time.Duration{Y: 10, M: 5, D: 8, TH: 5, TM: 10, TS: 6}},
	}

	for _, c := range cases {
		t.Run(c.from, func(t *testing.T) {
			got, err := ParseISO8601Duration(c.from)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}
