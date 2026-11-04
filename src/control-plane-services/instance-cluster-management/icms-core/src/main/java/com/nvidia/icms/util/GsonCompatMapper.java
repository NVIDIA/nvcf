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
package com.nvidia.icms.util;

import com.fasterxml.jackson.annotation.JsonInclude;
import lombok.experimental.UtilityClass;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;

/**
 * Jackson 3 replacement for the previously used {@code new Gson()} at former Gson boundaries
 * (NATS/SNS payloads, Cassandra-persisted metadata strings, and the HTTP error body). The only
 * Gson default that differs from ICMS' primary {@code JsonMapper} for these payloads is null
 * omission, so this mapper sets {@code NON_NULL} inclusion to keep the emitted JSON
 * byte-compatible with the historical Gson output. Both value inclusion (null bean properties) and
 * content inclusion (null {@code Map} values) are set to {@code NON_NULL}, because Gson omits both;
 * several boundaries serialize metadata {@code Map}s whose values can be null. Unknown properties
 * are ignored on read to match Gson's lenient deserialization. Dates are written as ISO-8601 rather
 * than timestamps, mirroring the rest of the ICMS Jackson configuration
 */
@UtilityClass
public class GsonCompatMapper {

    private static final JsonMapper MAPPER =
            JsonMapper.builder()
                    .changeDefaultPropertyInclusion(
                            v -> v.withValueInclusion(JsonInclude.Include.NON_NULL)
                                    .withContentInclusion(JsonInclude.Include.NON_NULL))
                    .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                    .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                    .build();

    /**
     * Serializes the given value to JSON with Gson-compatible semantics (null fields omitted).
     *
     * @param value the object to serialize
     * @return the JSON representation
     */
    public static String toJson(Object value) {
        return MAPPER.writeValueAsString(value);
    }

    /**
     * Deserializes the given JSON into an instance of the supplied type, ignoring unknown
     * properties like Gson did.
     *
     * @param json the JSON to parse
     * @param type the target type
     * @param <T>  the target type parameter
     * @return the deserialized instance, or {@code null} if {@code json} is null
     */
    public static <T> T fromJson(String json, Class<T> type) {
        if (json == null) {
            return null;
        }
        return MAPPER.readValue(json, type);
    }
}
