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

package com.nvidia.apikeys.converters;

import static com.nvidia.apikeys.TestData.INTROSPECTION_RESPONSE_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_LOOKUP;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_NO_SECRET;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_SECRET;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_RESPONSE_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_RESPONSE_EMPTY;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.apikeys.TestData;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyResponseBuilderTest {

    private final JsonMapper jsonMapper = JsonUtils.getRequestResponseJsonMapper();
    private final KeyResponseBuilder keyResponseBuilder = new KeyResponseBuilder(jsonMapper);


    @Test
    void toResponse_shouldConvert() {
        assertThat(keyResponseBuilder.toDto(TestData.KEY_VO_1, TestData.GENERATED_KEY_VO_1))
                .isEqualTo(KEY_DTO_1_SECRET);
    }

    @Test
    void toResponse_throwsIfJsonInvalid() {
        KeyVo keyVo = TestData.KEY_VO_1.toBuilder().authorizations("invalid").build();

        assertThrowsExceptionWithDetails(
                UnprocessableEntityException.class,
                () -> keyResponseBuilder.toDto(keyVo, TestData.GENERATED_KEY_VO_1),
                "Failed to build authorizations json");
    }

    @Test
    void toDto() {
        assertThat(keyResponseBuilder.toDto(KEY_VO_1)).isEqualTo(KEY_DTO_1_NO_SECRET);
    }

    @Test
    void toLookupDto() {
        assertThat(keyResponseBuilder.toLookupDto(KEY_VO_1)).isEqualTo(KEY_DTO_1_LOOKUP);
    }

    @Test
    void toListResponse() {
        assertThat(keyResponseBuilder.toListResponse(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1)))
                .isEqualTo(LIST_KEYS_RESPONSE_1);
    }

    @Test
    void toListResponse_toleratesEmptyList() {
        assertThat(keyResponseBuilder.toListResponse(List.of()))
                .isEqualTo(LIST_KEYS_RESPONSE_EMPTY);
    }

    @Test
    void toIntrospectionResponse() {
        assertThat(keyResponseBuilder.toIntrospectionResponse(KEY_VO_1))
                .isEqualTo(INTROSPECTION_RESPONSE_1);
    }
}
