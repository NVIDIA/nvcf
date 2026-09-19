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

class AuthzResponseTest {

    private final JsonMapper jsonMapper = JsonUtils.getRequestResponseJsonMapper();

    // The mapper's default property inclusion is NON_NULL; both rate fields being null is
    // itself meaningful (no rate configured) and must not collapse to a bare {}.
    @Test
    void serializes_bothNullRateFields_asExplicitNulls() throws Exception {
        String json = jsonMapper.writeValueAsString(
                AuthzResponse.AccountTokenRateLimit.builder().build());

        assertThat(json).isEqualTo(
                "{\"inputTokenRateLimit\":null,\"outputTokenRateLimit\":null}");
    }

    @Test
    void serializes_onePresentRateField_alongsideExplicitNull() throws Exception {
        String json = jsonMapper.writeValueAsString(
                AuthzResponse.AccountTokenRateLimit.builder()
                        .inputTokenRateLimit("5000000-M")
                        .build());

        assertThat(json).isEqualTo(
                "{\"inputTokenRateLimit\":\"5000000-M\",\"outputTokenRateLimit\":null}");
    }
}
