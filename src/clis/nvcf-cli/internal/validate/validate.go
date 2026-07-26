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

package validate

import "regexp"

// iso8601DurationRe matches valid ISO 8601 period strings (e.g. PT4H, P1DT30M).
// The NVCF API deserializes these into java.time.Duration; invalid strings produce
// a generic 400 with no field name. Validate before sending so users get a clear message.
var iso8601DurationRe = regexp.MustCompile(
	`^P(?:\d+Y)?(?:\d+M)?(?:\d+W)?(?:\d+D)?(?:T(?:\d+H)?(?:\d+M)?(?:\d+(?:\.\d+)?S)?)?$`)

// IsISO8601Duration reports whether s is a non-empty, well-formed ISO 8601 duration.
func IsISO8601Duration(s string) bool {
	return s != "" && iso8601DurationRe.MatchString(s)
}
