// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"testing"
)

func TestNextPageFromHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		page    int
		ok      bool
	}{
		{
			name: "GitLab header takes precedence",
			headers: http.Header{
				"X-Next-Page": {"7"},
				"Link":        {`<https://example.test/items?page=9>; rel="next"`},
			},
			page: 7,
			ok:   true,
		},
		{
			name: "malformed GitLab header falls through to Link",
			headers: http.Header{
				"X-Next-Page": {"invalid"},
				"Link":        {`<https://example.test/items?page=3>; rel="next"`},
			},
			page: 3,
			ok:   true,
		},
		{
			name: "non-positive GitLab header falls through to Link",
			headers: http.Header{
				"X-Next-Page": {"0"},
				"Link":        {`<https://example.test/items?page=4>; rel="next"`},
			},
			page: 4,
			ok:   true,
		},
		{
			name: "multiple Link fields",
			headers: http.Header{
				"Link": {
					`<https://example.test/items?page=1>; rel="prev"`,
					`<https://example.test/items?page=5>; rel="prev next"`,
				},
			},
			page: 5,
			ok:   true,
		},
		{
			name: "no next page",
			headers: http.Header{
				"Link": {`<https://example.test/items?page=1>; rel="prev"`},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, ok := nextPageFromHeaders(test.headers)
			if page != test.page || ok != test.ok {
				t.Fatalf("nextPageFromHeaders() = (%d, %t), want (%d, %t)", page, ok, test.page, test.ok)
			}
		})
	}
}

func TestLinkHasRelNext(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   bool
	}{
		{name: "quoted relation list", params: []string{` rel="prev next"`}, want: true},
		{name: "unquoted next", params: []string{"rel=next"}, want: true},
		{name: "next after another parameter", params: []string{"type=application/json", ` rel="next"`}, want: true},
		{name: "different relation", params: []string{`rel="prev last"`}},
		{name: "no relation", params: []string{"type=application/json"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := linkHasRelNext(test.params); got != test.want {
				t.Fatalf("linkHasRelNext(%v) = %t, want %t", test.params, got, test.want)
			}
		})
	}
}
