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
package com.nvidia.ess.config;

// JsonInclude is a Jackson *annotation*, in the com.fasterxml.jackson.annotation package (distinct
// from the tools.jackson core/databind packages used below).
import com.fasterxml.jackson.annotation.JsonInclude;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;

@Configuration
public class BeanConfig {

    // The bean's return type MUST be JsonMapper (not the ObjectMapper supertype). Spring's reactive
    // HTTP codec autoconfiguration (CodecsAutoConfiguration$JacksonJsonCodecConfiguration) and
    // JacksonAutoConfiguration are keyed on tools.jackson.databind.json.JsonMapper: the codec
    // customizer takes a JsonMapper and builds the JacksonJsonDecoder from it. A bean declared as the
    // ObjectMapper supertype is not matched as a JsonMapper, so the framework auto-creates its own
    // default (lenient) JsonMapper and wires THAT into the WebFlux request-body decoder — bypassing
    // every setting below (notably FAIL_ON_UNKNOWN_PROPERTIES). Declaring JsonMapper makes the codec
    // use this mapper. JsonMapper is-an ObjectMapper, so existing `@Autowired ObjectMapper` injection
    // points are unaffected.
    @Bean
    public JsonMapper objectMapper() {
        // Three explicit settings:
        //  - Property inclusion NON_NULL for both values and content, so null fields are omitted from
        //    serialized output.
        //  - WRITE_DATES_AS_TIMESTAMPS disabled, so java.time values serialize as ISO-8601 strings.
        //  - FAIL_ON_UNKNOWN_PROPERTIES enabled, so unknown/misspelled request-body properties are
        //    rejected with 400 rather than silently ignored (which would let a malformed body succeed).
        return JsonMapper.builder()
                .findAndAddModules()
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .enable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .changeDefaultPropertyInclusion(v -> v.withValueInclusion(JsonInclude.Include.NON_NULL)
                        .withContentInclusion(JsonInclude.Include.NON_NULL))
                .build();
    }
}
