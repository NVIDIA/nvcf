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

// Copied and modified from
// https://github.com/senseyeio/duration/blob/7c2a214ada4602c1d0638fb1abdbf8c6f25d0967/duration.go

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var pattern = regexp.MustCompile(`^P((?P<year>\d+)Y)?((?P<month>\d+)M)?((?P<week>\d+)W)?((?P<day>\d+)D)?(T((?P<hour>\d+)H)?((?P<minute>\d+)M)?((?P<second>\d+)S)?)?$`)

const (
	year  time.Duration = 365 * 24 * time.Hour
	month time.Duration = 30 * 24 * time.Hour
	week  time.Duration = 7 * 24 * time.Hour
	day   time.Duration = 24 * time.Hour
)

// ParseISO8601Duration parses an ISO8601 duration string into a time.Duration.
// 1 Year == 365 days
// 1 Month == 30 days
// 1 Week == 7 days
// 1 Day == 24 hours
//
// https://en.wikipedia.org/wiki/ISO_8601#Durations
func ParseISO8601Duration(from string) (td time.Duration, err error) {
	if !pattern.MatchString(from) {
		return 0, errors.New("could not parse duration string")
	}

	match := pattern.FindStringSubmatch(from)

	for i, name := range pattern.SubexpNames() {
		part := match[i]
		if i == 0 || name == "" || part == "" {
			continue
		}

		val, err := strconv.Atoi(part)
		if err != nil {
			return 0, err
		}
		switch name {
		case "year":
			td += time.Duration(val) * year
		case "month":
			td += time.Duration(val) * month
		case "week":
			td += time.Duration(val) * week
		case "day":
			td += time.Duration(val) * 24 * time.Hour
		case "hour":
			td += time.Duration(val) * time.Hour
		case "minute":
			td += time.Duration(val) * time.Minute
		case "second":
			td += time.Duration(val) * time.Second
		default:
			return 0, fmt.Errorf("unknown field %s", name)
		}
	}

	return td, nil
}
