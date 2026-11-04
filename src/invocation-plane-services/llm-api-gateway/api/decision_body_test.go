// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/config"
)

// TestDecisionRoutesForwardBodyBytesExceptModel checks that the backend gets
// the client's exact bytes, with only the top-level model value replaced.
func TestDecisionRoutesForwardBodyBytesExceptModel(t *testing.T) {
	t.Parallel()

	const template = "{\n  \"z\" : 1,\t\"model\":  %s ,\n" +
		`  "a": {"q": "<b>&</b>", "model": "fn-alpha/nested-is-kept", "n": 9007199254740993, "f": 1.50},` + "\n" +
		`  "e": "caf\u00e9 \"quoted\" \/ ` + "\u00e9" + `",` +
		`  "list": [ 1 , [ ] , { } , null , true ],` +
		`  "dup": 1, "dup": 2` + "\n}\n"
	for _, route := range decisionRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()

			got := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got <- body
				_, _ = io.WriteString(w, `{}`)
			}))
			defer upstream.Close()

			e := decisionTestAPI(t, upstream.URL, config.Default(), nil, decisionSpecs(route.path))
			sent := strings.Replace(template, "%s", `"fn-alpha/company-name/model-name"`, 1)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, decisionRequest(route, sent))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			want := strings.Replace(template, "%s", `"`+decisionTestModel+`"`, 1)
			require.Equal(t, want, string(<-got))
		})
	}
}

func TestSpliceDecisionModel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, body, want string
	}{
		{"only the model value changes", `{"b":1,"model":"fn/x","a":2}`, `{"b":1,"model":"m<&>","a":2}`},
		{"escaped key still names model and stays escaped", `{"a":1,"mod\u0065l":"fn/x","b":2}`, `{"a":1,"mod\u0065l":"m<&>","b":2}`},
		{"every exact duplicate is replaced", `{"model":"fn/x","model":"fn/y"}`, `{"model":"m<&>","model":"m<&>"}`},
		{"model value with escapes", `{"model":"fn/\"x\\"}`, `{"model":"m<&>"}`},
		{"nested model is kept", `{"s":{"model":"fn/x"},"model":"fn/x"}`, `{"s":{"model":"fn/x"},"model":"m<&>"}`},
		{"strings with braces are skipped", `{"s":"}{\"model\":","model":"fn/x"}`, `{"s":"}{\"model\":","model":"m<&>"}`},
		{"no model member is unchanged", `{"input":"x"}`, `{"input":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			members, err := scanDecisionModelMembers([]byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.want, string(members.splice([]byte(tc.body), "m<&>")))
		})
	}
}

func TestScanDecisionModelMembersRejectsCaseVariants(t *testing.T) {
	t.Parallel()

	_, err := scanDecisionModelMembers([]byte(`{"model":"fn/x","MODEL":"fn/y"}`))
	require.ErrorContains(t, err, `field "model" is sent more than once with different letter case`)
	_, err = scanDecisionModelMembers([]byte(`{"state":{"model":"a","Model":"b"},"model":"fn/x"}`))
	require.NoError(t, err, "only top-level members are bound")
}
