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

package com.nvidia.boot.core.bootstrap;

import java.util.Arrays;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.cloud.bootstrap.BootstrapConfiguration;
import org.springframework.core.env.Environment;

@Slf4j
@BootstrapConfiguration
public class MiscBootstrapConfiguration {
    private static final String MESG_ACTIVE_PROFILES = "The following {}: {}";
    private static final String MESG_MISSING_ACTIVE_PROFILES =
            "Property 'spring.profiles.active' must be defined";

    public MiscBootstrapConfiguration(Environment environment) {
        var activeProfiles = Arrays.asList(environment.getActiveProfiles());
        if (activeProfiles.isEmpty()) {
            log.error(MESG_MISSING_ACTIVE_PROFILES);
            throw new IllegalArgumentException(MESG_MISSING_ACTIVE_PROFILES);
        }

        var message = (activeProfiles.size() == 1) ? "1 profile is active: "
                : activeProfiles.size() + " profiles are active: ";
        var profiles = activeProfiles.stream()
                .map(profile -> "\"" + profile + "\"")
                .collect(Collectors.toList());
        log.info(MESG_ACTIVE_PROFILES, message, StringUtils.join(profiles, ", "));
    }
}
