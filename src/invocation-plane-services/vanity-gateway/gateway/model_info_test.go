/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	config "ai-api-gateway-service/gateway_config"

	"github.com/stretchr/testify/require"
)

func metadataConfig(t *testing.T) *config.GatewayConfig {
	t.Helper()
	var cfg config.GatewayConfig
	require.NoError(t, json.Unmarshal([]byte(`{"v2config":{
	  "openai":{"host":"api.test","chatCompletions":{
	    "chat":{"modelName":"example/shared","modelInfoFields":{"custom_flag":true,"winner":"chat","context_window":1048576,"large":18446744073709551615,"decimal":0.12345678901234567890123456789,"nested":{"items":[true,null,"text",42]},"nullable":null}},
	    "private":{"modelName":"private/hidden","modelInfoFields":{"secret":true}},
	    "expired":{"modelName":"example/expired","eol":"2000-01-01T00:00:00Z","modelInfoFields":{"custom_flag":true}},
	    "offline":{"modelName":"example/offline","offlineMessage":"back soon","modelInfoFields":{"custom_flag":true}}
	  },"completions":{"shared":{"modelName":"example/shared","modelInfoFields":{"winner":"completion","completion":true}}}},
	  "anthropicMessages":{"shared":{"modelName":"example/shared","modelInfoFields":{"winner":"message","message":true}}}
	}}`), &cfg))
	require.NoError(t, cfg.Validate())
	return &cfg
}

func metadataMux(t *testing.T, cfg *config.GatewayConfig) http.Handler {
	t.Helper()
	mux, err := buildChiMux(cfg, Config{NvcfApiEndpoint: "http://upstream.invalid", PrivateModelNameRegexPattern: "^private/"})
	require.NoError(t, err)
	return mux
}

func responseFields(t *testing.T, mux http.Handler, target string) map[string]json.RawMessage {
	t.Helper()
	rec := getModels(t, mux, target, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
	return fields
}

func TestModelInfoListDetailAndVisibility(t *testing.T) {
	mux := metadataMux(t, metadataConfig(t))
	list := responseFields(t, mux, "/v1/models")
	var entries []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(list["data"], &entries))
	require.Len(t, entries, 2)
	for _, entry := range entries {
		var id string
		require.NoError(t, json.Unmarshal(entry["id"], &id))
		require.NotContains(t, []string{"private/hidden", "example/expired"}, id)
		if id != "example/shared" {
			continue
		}
		detail := responseFields(t, mux, "/v1/models/"+id)
		require.Equal(t, entry, detail)
		for key, expected := range map[string]string{
			"custom_flag": "true", "winner": `"chat"`, "context_window": "1048576",
			"large": "18446744073709551615", "decimal": "0.12345678901234567890123456789",
			"nested": `{"items":[true,null,"text",42]}`, "nullable": "null",
			"completion": "true", "message": "true",
		} {
			require.JSONEq(t, expected, string(detail[key]), key)
		}
		require.JSONEq(t, `["/v1/chat/completions","/v1/completions","/v1/messages"]`, string(detail["supported_endpoints"]))
	}
	for target, status := range map[string]int{
		"/v1/models/private/hidden":  http.StatusNotFound,
		"/v1/models/example/expired": http.StatusGone,
		"/v1/models/example/offline": http.StatusServiceUnavailable,
	} {
		require.Equal(t, status, getModels(t, mux, target, false).Code)
	}
}

func TestModelInfoLeavesAnthropicShapeUnchanged(t *testing.T) {
	mux := metadataMux(t, metadataConfig(t))
	detail := getModels(t, mux, "/v1/models/example/shared", true)
	require.Equal(t, http.StatusOK, detail.Code)
	require.JSONEq(t, `{"type":"model","id":"example/shared","display_name":"example/shared","created_at":"1993-04-26T02:13:23Z"}`, detail.Body.String())
	list := getModels(t, mux, "/v1/models", true)
	require.Equal(t, http.StatusOK, list.Code)
	require.JSONEq(t, `{"data":[{"type":"model","id":"example/shared","display_name":"example/shared","created_at":"1993-04-26T02:13:23Z"}],"has_more":false,"first_id":"example/shared","last_id":"example/shared"}`, list.Body.String())
}

func TestModelInfoEmptyCompatibility(t *testing.T) {
	for _, suffix := range []string{"", `,"modelInfoFields":null`, `,"modelInfoFields":{}`} {
		var cfg config.GatewayConfig
		require.NoError(t, json.Unmarshal([]byte(`{"v2config":{"openai":{"host":"api.test","chatCompletions":{"entry":{"modelName":"example/model"`+suffix+`}}}}}`), &cfg))
		mux := metadataMux(t, &cfg)
		require.Equal(t, `{"id":"example/model","object":"model","created":735790403,"owned_by":"example"}`+"\n", getModels(t, mux, "/v1/models/example/model", false).Body.String())
		require.Equal(t, `{"object":"list","data":[{"id":"example/model","object":"model","created":735790403,"owned_by":"example"}]}`+"\n", getModels(t, mux, "/v1/models", false).Body.String())
	}
}

func TestModelInfoSnapshotAndConcurrentReads(t *testing.T) {
	cfg := metadataConfig(t)
	mux := metadataMux(t, cfg)
	fields := cfg.OpenAI.ChatCompletions["chat"].ModelInfoFields
	fields["custom_flag"][0] = 'f'
	delete(fields, "nested")
	baseline := getModels(t, mux, "/v1/models", false).Body.String()
	require.Contains(t, baseline, `"custom_flag":true`)
	require.Contains(t, baseline, `"nested":`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				require.Equal(t, baseline, getModels(t, mux, "/v1/models", false).Body.String())
				getModels(t, mux, "/v1/models/example/shared", false)
				getModels(t, mux, "/v1/models/example/shared", true)
			}
		})
	}
	wg.Wait()
}

func TestModelInfoReloadSwapsSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapping.yaml")
	write := func(fields string) {
		t.Helper()
		data := fmt.Sprintf(`v2config:
  openai:
    host: api.test
    chatCompletions:
      z-first:
        modelName: example/model
        modelInfoFields: %s
      a-second:
        modelName: example/model
        modelInfoFields: {context_window: 1, extra: true}
`, fields)
		// Atomic replacement follows projected-config updates and avoids partial reads.
		require.NoError(t, os.WriteFile(path+".next", []byte(data), 0600))
		require.NoError(t, os.Rename(path+".next", path))
	}
	write("{custom_flag: true, context_window: 1048576}")
	mappings, err := config.SetupConfigWithConfigPath(path)
	require.NoError(t, err)
	serverCfg := Config{NvcfApiEndpoint: "http://upstream.invalid", PrivateModelNameRegexPattern: "^private/"}
	router, err := buildRouter(mappings.Get(), serverCfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	swapRouterWhenNotified(ctx, mappings, serverCfg, router)
	oldMux := metadataMux(t, mappings.Get())
	require.Equal(t, "1048576", string(responseFields(t, router, "/v1/models/example/model")["context_window"]))
	write("{custom_flag: false, context_window: 2097152}")
	require.Eventually(t, func() bool {
		return string(responseFields(t, router, "/v1/models/example/model")["context_window"]) == "2097152"
	}, 5*time.Second, 10*time.Millisecond)
	detail := responseFields(t, router, "/v1/models/example/model")
	require.Equal(t, "false", string(detail["custom_flag"]))
	require.Equal(t, "true", string(detail["extra"]))
	var list []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(responseFields(t, router, "/v1/models")["data"], &list))
	require.Equal(t, detail, list[0])
	require.Equal(t, "1048576", string(responseFields(t, oldMux, "/v1/models/example/model")["context_window"]))

	// Invalid metadata must leave the last accepted config and router intact.
	write("{id: forbidden}")
	require.Never(t, func() bool {
		return string(responseFields(t, router, "/v1/models/example/model")["context_window"]) != "2097152"
	}, 200*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, "2097152", string(mappings.Get().OpenAI.ChatCompletions["z-first"].ModelInfoFields["context_window"]))
	write("{custom_flag: true, context_window: 4194304}")
	require.Eventually(t, func() bool {
		return string(responseFields(t, router, "/v1/models/example/model")["context_window"]) == "4194304"
	}, 5*time.Second, 10*time.Millisecond)
}

func TestModelInfoSharedFixtureResponses(t *testing.T) {
	mappings, err := config.SetupConfigWithConfigPath("../testdata/model_info/valid.yaml")
	require.NoError(t, err)
	mux := metadataMux(t, mappings.Get())
	expectedData, err := os.ReadFile("../testdata/model_info/expected.json")
	require.NoError(t, err)
	var expected map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expectedData, &expected))
	var entries []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(responseFields(t, mux, "/v1/models")["data"], &entries))
	require.Len(t, entries, len(expected))
	for _, entry := range entries {
		var id string
		require.NoError(t, json.Unmarshal(entry["id"], &id))
		detail := responseFields(t, mux, "/v1/models/"+id)
		require.Equal(t, entry, detail)
		for key, value := range expected[id] {
			require.JSONEq(t, string(value), string(detail[key]), id+"/"+key)
		}
	}
}
