/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.nvidia.apikeys.dto.authz;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.utils.JsonUtils;
import org.junit.jupiter.api.Test;
import tools.jackson.databind.json.JsonMapper;

class ApiKeyInputTest {

    private final JsonMapper jsonMapper = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void deserializes_canonicalApiKeyField() throws Exception {
        ApiKeyInput input = jsonMapper.readValue(
                "{\"apiKey\":\"key-123\"}", ApiKeyInput.class);

        assertThat(input.getApiKey()).isEqualTo("key-123");
    }

    @Test
    void serializes_usingCanonicalName() throws Exception {
        String json = jsonMapper.writeValueAsString(
                ApiKeyInput.builder().apiKey("key-789").build());

        assertThat(json).isEqualTo("{\"apiKey\":\"key-789\"}");
    }
}
