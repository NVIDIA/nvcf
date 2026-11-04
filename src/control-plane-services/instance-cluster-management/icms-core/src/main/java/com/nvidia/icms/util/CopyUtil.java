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

import com.nvidia.icms.errors.IcmsInternalServerException;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class CopyUtil {

    private static final ObjectMapper objectMapper =
            JsonMapper.builder()
                    .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                    .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                    .build();

    @SuppressWarnings("unchecked")
    public static <T> T deepCopy(T object) {
        Class<T> type = (Class<T>) object.getClass();
        try {
            String json = objectMapper.writeValueAsString(object);
            return objectMapper.readValue(json, type);
        } catch (Exception e) {
            String errorMsg = String.format("Failed to create deep copy of request object for %s, error: %s", type.getName(), e.getMessage());
            log.error(errorMsg);
            throw new IcmsInternalServerException(errorMsg, e);
        }
    }
}
