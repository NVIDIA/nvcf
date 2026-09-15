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
package com.nvidia.ess.auth.jwk;

import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;
import org.springframework.validation.annotation.Validated;

@Configuration
@ConfigurationProperties(prefix = "ess.cache.jwk")
@Data
@Validated
public class JwkCacheProperties {
    @Min(1)
    private int maxSize = 1024;

    @Min(1)
    private int initSize = 128; // # max # of entries

    @NotNull
    private Duration expireAfterWrite = Duration.ofDays(1);

    // Must be lower than expireAfterWrite to be effective
    // Defaulting to 1h. Cache-Control: max-age=3600 on tested JWKS services commonly polled by ESS
    @NotNull
    private Duration refreshAfterWrite = Duration.ofHours(1);
}