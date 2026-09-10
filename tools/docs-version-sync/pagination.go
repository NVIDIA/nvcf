// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func nextPageFromHeaders(headers http.Header) (int, bool) {
	if value := strings.TrimSpace(headers.Get("X-Next-Page")); value != "" {
		if page, err := strconv.Atoi(value); err == nil && page > 0 {
			return page, true
		}
	}
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
