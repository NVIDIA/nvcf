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

package com.nvidia.apikeys.utils;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;

import org.junit.jupiter.api.function.Executable;
import org.springframework.web.ErrorResponseException;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

public class TestUtils {

    public static void assertJsonEquals(JsonMapper jsonMapper, String expectedJson, String actualJson)
            throws JacksonException {
        assertThat(jsonMapper.readTree(actualJson)).isEqualTo(jsonMapper.readTree(expectedJson));
    }

    public static <T extends ErrorResponseException> T assertThrowsExceptionWithDetails(
            Class<T> expectedThrowable, Executable runnable, String expectedErrorDetails) {
        T exception = assertThrows(expectedThrowable, runnable);
        assertThat(exception.getBody().getDetail()).isEqualTo(expectedErrorDetails);
        return exception;
    }

}
