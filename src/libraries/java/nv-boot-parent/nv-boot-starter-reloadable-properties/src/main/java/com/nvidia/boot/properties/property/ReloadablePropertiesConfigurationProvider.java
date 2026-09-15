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

package com.nvidia.boot.properties.property;

import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.FILE;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.POLL_DURATION;

import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.convert.DurationStyle;
import org.springframework.core.env.Environment;

/**
 * Reads file path and poll duration from {@code nv-boot.reloadable-properties} properties
 * in the Environment.
 */
public class ReloadablePropertiesConfigurationProvider {

    private static final int DEFAULT_DELAY_SECONDS = 300;

    private final Environment environment;

    public ReloadablePropertiesConfigurationProvider(Environment environment) {
        this.environment = environment;
    }

    public String getPropertiesFilePath() {
        var file = environment.getProperty(FILE);
        if (StringUtils.isBlank(file)) {
            var mesg = "'%s' must be set when reloadable properties are enabled"
                    .formatted(FILE);
            throw new IllegalStateException(mesg);
        }
        return file;
    }

    public int getDelaySeconds() {
        var pollDurationStr = environment.getProperty(POLL_DURATION);
        if (StringUtils.isBlank(pollDurationStr)) {
            return DEFAULT_DELAY_SECONDS;
        }
        return (int) DurationStyle.detectAndParse(pollDurationStr).getSeconds();
    }
}
