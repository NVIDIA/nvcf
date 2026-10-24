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
package com.nvidia.icms.configuration.bean;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.http.ProblemDetail;
import org.springframework.http.converter.json.ProblemDetailJacksonMixin;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.module.blackbird.BlackbirdModule;

/**
 * Primary Jackson 3 mapper used by Spring MVC and injected wherever an {@code ObjectMapper}/
 * {@code JsonMapper} is required. Preserves the historical ICMS serialization behavior:
 * unknown properties are ignored on read and dates are written as ISO-8601 (not timestamps).
 * Null values are kept (Jackson's default inclusion) so existing API responses are unchanged.
 * {@code FAIL_ON_NULL_FOR_PRIMITIVES} is disabled to retain the Jackson 2 contract where an
 * absent/null JSON value for a primitive field (e.g. optional {@code gpuCount}) coerces to the
 * default value instead of failing the request.
 */
@Configuration
public class JacksonConfiguration {

    @Bean
    @Primary
    public JsonMapper jsonMapper() {
        return JsonMapper.builder()
                .addModule(new BlackbirdModule())
                .addMixIn(ProblemDetail.class, ProblemDetailJacksonMixin.class)
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .disable(DeserializationFeature.FAIL_ON_NULL_FOR_PRIMITIVES)
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .build();
    }
}
