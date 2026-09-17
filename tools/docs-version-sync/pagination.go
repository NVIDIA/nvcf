// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// nextPageFromHeaders returns the next page declared by RFC 8288 Link headers.
func nextPageFromHeaders(headers http.Header) (int, bool) {
	for _, header := range headers.Values("Link") {
		for _, link := range strings.Split(header, ",") {
			parts := strings.Split(link, ";")
			if len(parts) < 2 || !linkHasRelNext(parts[1:]) {
				continue
			}
			rawURL := strings.Trim(strings.TrimSpace(parts[0]), "<>")
			parsed, err := url.Parse(rawURL)
			if err != nil {
				continue
			}
			page, err := strconv.Atoi(parsed.Query().Get("page"))
			if err == nil && page > 0 {
				return page, true
			}
		}
	}
	return 0, false
}

// linkHasRelNext reports whether Link parameters include the next relation.
func linkHasRelNext(params []string) bool {
	for _, param := range params {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(param, "rel=") {
			continue
		}
		rel := strings.Trim(strings.TrimPrefix(param, "rel="), `"`)
		for _, value := range strings.Fields(rel) {
			if value == "next" {
				return true
			}
		}
	}
	return false
}
