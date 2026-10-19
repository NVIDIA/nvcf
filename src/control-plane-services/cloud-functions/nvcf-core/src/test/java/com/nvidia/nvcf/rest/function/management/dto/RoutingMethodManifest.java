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
package com.nvidia.nvcf.rest.function.management.dto;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.stream.Stream;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.PropertyNamingStrategies;
import tools.jackson.databind.json.JsonMapper;

/**
 * Stargate's routing method manifest: every routing method name, alias, and routing-expression
 * parameter the LLM request router accepts. The router owns these names, so tests read them here
 * instead of copying them.
 */
record RoutingMethodManifest(List<String> commonParameters, List<Method> methods) {

    /**
     * Repository-relative path. The nvcf-core test target lists the file as data, which places it
     * at the same path under the runfiles root, the working directory of a Bazel test.
     */
    static final Path PATH = Path.of("src/libraries/rust/stargate/routing_methods.json");

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder()
            .propertyNamingStrategy(PropertyNamingStrategies.SNAKE_CASE)
            .enable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
            .build();

    /**
     * One routing method.
     *
     * @param name canonical name
     * @param aliases other names the router accepts for the method
     * @param parameters expression parameters the method accepts beyond the common ones
     */
    record Method(String name, List<String> aliases, List<String> parameters) {

        /** The canonical name followed by every alias. */
        Stream<String> spellings() {
            return Stream.concat(Stream.of(name), aliases.stream());
        }
    }

    static RoutingMethodManifest load() {
        try {
            return JSON_MAPPER.readValue(Files.readString(PATH), RoutingMethodManifest.class);
        } catch (IOException e) {
            throw new UncheckedIOException(
                    "Cannot read " + PATH.toAbsolutePath()
                            + "; list it in the test target's data", e);
        }
    }

    /** Every expression parameter the method accepts: its own, then the common ones. */
    List<String> parameters(Method method) {
        return Stream.concat(method.parameters().stream(), commonParameters.stream()).toList();
    }
}
