// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package restful

import (
	"fmt"
	"regexp"
)

var (
	customVerbReg = regexp.MustCompile(":([A-Za-z]+)$")
)

func hasCustomVerb(routeToken string) bool {
	return customVerbReg.MatchString(routeToken)
}

func isMatchCustomVerb(routeToken string, pathToken string) bool {
	rs := customVerbReg.FindStringSubmatch(routeToken)
	if len(rs) < 2 {
		return false
	}

	customVerb := rs[1]
	specificVerbReg := regexp.MustCompile(fmt.Sprintf(":%s$", customVerb))
	return specificVerbReg.MatchString(pathToken)
}

func removeCustomVerb(str string) string {
	return customVerbReg.ReplaceAllString(str, "")
}
