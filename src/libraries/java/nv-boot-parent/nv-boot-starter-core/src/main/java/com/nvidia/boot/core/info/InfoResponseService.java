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

package com.nvidia.boot.core.info;

import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.env.Environment;

@RequiredArgsConstructor
public class InfoResponseService {

    private static final String UNKNOWN = "unknown";

    private final Environment environment;

    public InfoResponse getInfo() {
        return new InfoResponse(
                resolve("spring.application.name"),
                resolve("spring.application.version"),
                resolve("app.git.commit.full"));
    }

    private String resolve(String key) {
        String value = environment.getProperty(key);
        return StringUtils.isBlank(value) ? UNKNOWN : value;
    }

    public record InfoResponse(String service, String version, String commit) {
    }
}
