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
package com.nvidia.ess.util;

import com.nimbusds.jose.JOSEObject;
import com.nimbusds.jose.util.Base64URL;
import lombok.SneakyThrows;
import org.junit.jupiter.api.Assertions;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.json.JsonMapper;

public class PrintUtils {

    private static final ObjectMapper objectMapper = JsonMapper.builder().findAndAddModules().build();

    @SneakyThrows
    private static String prettyPrintJsonString(String jsonString) {
        var json = objectMapper.readValue(jsonString, Object.class);
        return objectMapper.writerWithDefaultPrettyPrinter().writeValueAsString(json);
    }

    @SneakyThrows
    private static String prettyPrintJWTPart(Base64URL jwtPart) {
        return prettyPrintJsonString(jwtPart.decodeToString());
    }

    @SneakyThrows
    public static String signedJWTToString(String signedJWT) {
        var parts = JOSEObject.split(signedJWT);
        Assertions.assertEquals(3, parts.length, "Invalid signed JWT string: " + signedJWT);
        return "PART1(" + prettyPrintJWTPart(parts[0])
                + ")::PART2(" + prettyPrintJWTPart(parts[1])
                + ")::PART3(" + parts[2] + ")";
    }
}
