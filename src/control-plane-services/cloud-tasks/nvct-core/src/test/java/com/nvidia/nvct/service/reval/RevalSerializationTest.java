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
package com.nvidia.nvct.service.reval;

import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.SerializationFeature;
import com.nvidia.nvct.service.reval.RevalStubService.RevalValidateResponse;
import java.util.Arrays;
import java.util.List;
import lombok.SneakyThrows;
import org.junit.jupiter.api.Test;

class RevalSerializationTest {

    @Test
    @SneakyThrows
    void testEmptyRequestSerialization()  {
        var jsonMapper = JsonMapper.builder().build();

        var request = RevalStubService.RevalValidateRequest.builder().build();
        var json = jsonMapper.writeValueAsString(request);
        var expectedJson = "{}";

        assertThat(json).isEqualTo(expectedJson);
    }

    @Test
    @SneakyThrows
    void testRequestSerialization()  {
        var jsonMapper = JsonMapper.builder()
                .enable(SerializationFeature.ORDER_MAP_ENTRIES_BY_KEYS)
                .build();

        var configuration = jsonMapper
                .createObjectNode()
                .put("key", "value")
                .put("key2", 2);

        var request = RevalStubService.RevalValidateRequest.builder()
                .configuration(configuration)
                .helmChart("my_helm_chart")
                .build();

        var requestJson = jsonMapper.writeValueAsString(request);
        var expectedJson = """
                {"helmChart":"my_helm_chart","configuration":{"key":"value","key2":2}}""";

        assertThat(jsonMapper.readTree(requestJson)).isEqualTo(jsonMapper.readTree(expectedJson));
    }


    @Test
    @SneakyThrows
    void testResponseDeserialization() {
        var json = """
            {
                "valid": true,
                "validationErrors": ["Error 1","Error 2"],
                "internalErrors": ["Internal Error 1"]
            }
            """;

        var jsonMapper = JsonMapper.builder().build();
        var validationResult = jsonMapper.readValue(json, RevalValidateResponse.class);
        var expectedValidationResult = RevalValidateResponse.builder()
                .valid(true)
                .validationErrors(Arrays.asList("Error 1", "Error 2"))
                .internalErrors(List.of("Internal Error 1"))
                .build();

        assertThat(validationResult).isEqualTo(expectedValidationResult);
    }
}
