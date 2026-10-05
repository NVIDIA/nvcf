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
package com.nvidia.nvct.service.apikeys;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Policy;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Resource;
import java.util.List;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import tools.jackson.databind.json.JsonMapper;

class ApiKeyValidationResultTest {

    private static final JsonMapper JSON_MAPPER = new JsonMapper();

    @Test
    void deserializesOwnerNcaIdFromResponse() {
        var json = """
                {"allowed": true, "ncaId": "nca-1", "ownerNcaId": "owner-nca-1",
                 "ownerId": "owner-1",
                 "policy": {"resources": [], "scopes": [], "product": "nvct"}}
                """;

        var result = JSON_MAPPER.readValue(json, ApiKeyValidationResult.class);

        assertEquals("nca-1", result.ncaId());
        assertEquals("owner-nca-1", result.ownerNcaId());
    }

    @Test
    void validDoesNotRequireOwnerNcaId() {
        var result = new ApiKeyValidationResult(true, "nca-1", null, "owner-1",
                new Policy(List.of(), List.of(), "nvct"));

        assertNull(result.ownerNcaId());
        assertTrue(result.valid());
    }

    static Stream<Arguments> resourceMatchesTaskArgs() {
        var TID1 = UUID.fromString("3c000ce0-87a7-4a15-bf32-c77f088f2975");
        var TID2 = UUID.fromString("b58d839c-3cd6-4ea9-ae73-87de33dc0787");
        return Stream.of(
                Arguments.of(TID1.toString(), TID1, true),
                Arguments.of(TID1.toString(), TID2, false),
                Arguments.of(TID2.toString(), TID2, true),
                Arguments.of(TID2.toString(), TID1, false),
                Arguments.of("*", TID1, false), // invalid pattern
                Arguments.of(null, TID1, false), // invalid pattern
                Arguments.of("invalid-uuid", TID1, false) // invalid pattern
        );
    }

    @ParameterizedTest
    @MethodSource("resourceMatchesTaskArgs")
    void allAllowedTasks(String resourceId, UUID taskId, boolean expected) {
        var access = ApiKeyValidationResult.allAllowedTasks(
                List.of(new Resource("task", resourceId)));
        var actual = access.hasResourcesScopedForTask(taskId);
        assertEquals(expected, actual);
    }

    @Test
    void allAllowedTasksAccount() {
        var access = ApiKeyValidationResult.allAllowedTasks(
                List.of(new Resource("account-tasks", "*")));
        assertTrue(access.privateTasksAllowed());
    }

    @Test
    void allAllowedTasksAccountWrongID() {
        var access = ApiKeyValidationResult.allAllowedTasks(
                List.of(new Resource("account-tasks", UUID.randomUUID().toString())));
        assertFalse(access.privateTasksAllowed());
    }
}
