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
package com.nvidia.nvcf.service.apikeys;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import java.util.List;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

class ApiKeyValidationResultTest {

    static Stream<Arguments> resourceMatchesFunctionArgs() {
        var FID1 = UUID.fromString("3c000ce0-87a7-4a15-bf32-c77f088f2975");
        var FID2 = UUID.fromString("b58d839c-3cd6-4ea9-ae73-87de33dc0787");
        var FVID1 = UUID.fromString("00aa5dd3-e5bd-4259-8e96-d88887e6eb17");
        var FVID2 = UUID.fromString("a9c62a2f-457b-4183-9c0e-1fbb532e97a4");
        return Stream.of(Arguments.of("*", FID1, FVID1, false),
                         Arguments.of("*", FID1, null, false),
                         Arguments.of("*", FID2, FVID2, false),
                         Arguments.of("*", null, null, false),
                         Arguments.of("*", null, FVID2, false),
                         Arguments.of(FID1 + "/*", FID1, FVID1, true),
                         Arguments.of(FID1 + "/*", FID1, FVID2, true),
                         Arguments.of(FID1 + "/*", FID1, null, true),
                         Arguments.of(FID1 + "/*", null, FVID1, false),
                         Arguments.of(FID1 + "/*", FID2, FVID1, false),
                         Arguments.of(FID1 + "/*", FID2, null, false),
                         Arguments.of(FID1 + "/" + FVID1, FID1, FVID1, true),
                         Arguments.of(FID1 + "/" + FVID1, FID1, FVID2, false),
                         Arguments.of(FID1 + "/" + FVID1, FID2, FVID1, false),
                         Arguments.of(FID1 + "/" + FVID1, FID2, FVID2, false),
                         Arguments.of("*/*", FID1, FVID1, false), // invalid pattern
                         Arguments.of("*/", FID1, FVID1, false), // invalid pattern
                         Arguments.of(FID1 + "/", FID1, FVID1, false), // invalid pattern
                         Arguments.of(FID1.toString(), FID1, FVID1, false), // invalid pattern
                         Arguments.of(FID1.toString(), FID1, null, false), // invalid pattern
                         Arguments.of(null, FID1, null, false), // invalid pattern
                         Arguments.of(null, null, null, false), // invalid pattern
                         Arguments.of(FID1 + "/" + FVID1, FID1, null, false),
                         Arguments.of(FID1 + "/abc", FID1, FVID1, false) // invalid pattern
        );
    }

    @ParameterizedTest
    @MethodSource("resourceMatchesFunctionArgs")
    void allAllowedFunctions(
            String resourceId, UUID functionId, UUID functionVersionId, boolean expected) {
        var access = ApiKeyValidationResult.allAllowedFunctions(
                List.of(new Resource("function", resourceId)));
        var actual = access.hasResourcesScopedForFunction(functionId, functionVersionId);
        assertEquals(expected, actual);
    }

    @Test
    void allAllowedFunctionsAccount() {
        var access = ApiKeyValidationResult.allAllowedFunctions(
                List.of(new Resource("account-functions", "*")));
        assertTrue(access.privateFunctionsAllowed());
    }

    @Test
    void allAllowedFunctionsAccountWrongID() {
        var access = ApiKeyValidationResult.allAllowedFunctions(
                List.of(new Resource("account-functions", UUID.randomUUID().toString())));
        assertFalse(access.privateFunctionsAllowed());
    }

    @Test
    void allAllowedFunctionsAuthorized() {
        var access = ApiKeyValidationResult.allAllowedFunctions(
                List.of(new Resource("authorized-functions", "*")));
        assertTrue(access.azpFunctionsAllowed());
    }

    @Test
    void allAllowedFunctionsAuthorizedWrongID() {
        var access = ApiKeyValidationResult.allAllowedFunctions(
                List.of(new Resource("authorized-functions", UUID.randomUUID().toString())));
        assertFalse(access.azpFunctionsAllowed());
    }
}