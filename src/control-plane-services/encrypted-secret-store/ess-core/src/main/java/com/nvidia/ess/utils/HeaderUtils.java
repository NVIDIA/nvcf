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
package com.nvidia.ess.utils;

import static com.nvidia.ess.constants.Constants.X_ESS_REQUEST_ID_HEADER;

import java.util.List;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.server.reactive.ServerHttpRequest;

@UtilityClass
@Slf4j
public class HeaderUtils {
  
    private static String filterValidValueOrDefault(List<String> requestIds, String defaultValue) {
        return requestIds.stream()
                .filter(StringUtils::isNotBlank)
                .findFirst()
                .orElse(defaultValue);
    }

    public static String getRequestIdOrDefault(ServerHttpRequest request, String defaultValue) {
        var requestId = request.getHeaders().getOrEmpty(X_ESS_REQUEST_ID_HEADER);
        return filterValidValueOrDefault(requestId, defaultValue);
    }
}
