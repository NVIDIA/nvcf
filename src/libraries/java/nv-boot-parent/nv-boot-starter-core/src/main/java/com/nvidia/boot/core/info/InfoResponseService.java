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
import org.springframework.core.env.Environment;

/**
 * Builds the {@link InfoResponse} served by {@link InfoController}, reading {@code version} and
 * {@code commit} from the {@code nv-boot-git-properties} {@link Environment} property source
 * that {@code BootCoreEnvironmentPostProcessor} populates from {@code git.properties} at startup.
 */
@RequiredArgsConstructor
public class InfoResponseService {

    private static final String UNKNOWN = "unknown";

    private final Environment environment;

    public InfoResponse getInfo() {
        return new InfoResponse(
                environment.getProperty("spring.application.name", UNKNOWN),
                environment.getProperty("spring.application.version", UNKNOWN),
                environment.getProperty("app.git.commit.full", UNKNOWN));
    }
}
