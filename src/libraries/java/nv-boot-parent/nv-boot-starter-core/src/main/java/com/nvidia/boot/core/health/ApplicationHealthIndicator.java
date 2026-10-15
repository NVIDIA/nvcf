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

package com.nvidia.boot.core.health;

import java.util.Optional;
import org.springframework.boot.health.contributor.AbstractHealthIndicator;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.info.BuildProperties;
import org.springframework.core.env.Environment;

/**
 * Health indicator that adds application metadata to health response.
 */
public class ApplicationHealthIndicator extends AbstractHealthIndicator {

    private final Environment environment;
    private final Optional<BuildProperties> buildProperties;

    public ApplicationHealthIndicator(Environment environment,
                                      Optional<BuildProperties> buildProperties) {
        this.environment = environment;
        this.buildProperties = buildProperties;
    }

    @Override
    protected void doHealthCheck(Health.Builder builder) {
        var name = environment.getProperty("spring.application.name", "unknown");
        var profiles = String.join(",", environment.getActiveProfiles());
        var version = buildProperties
                .map(BuildProperties::getVersion)
                .orElse(environment.getProperty("spring.application.version", "unknown"));

        builder.up()
                .withDetail("application", name)
                .withDetail("application.profile", profiles)
                .withDetail("version", version);
    }
}
