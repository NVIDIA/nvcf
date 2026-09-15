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
package com.nvidia.icms.configuration;

import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Singleton-scoped Reactor Netty resource pools (ConnectionProvider + LoopResources)
 * shared across {@link org.springframework.cloud.context.config.annotation.RefreshScope @RefreshScope}
 * outbound HTTP clients.
 *
 * <p>The pool configuration ({@code MAX_CONNECTIONS}, {@code MAX_IDLE_TIME},
 * timeouts) is sourced from constants in {@link OAuth2ClientUtils} and never
 * changes at runtime, so the pool itself does not need to participate in
 * {@code @RefreshScope}. Keeping pools as singletons avoids per-refresh warmup cost.
 *
 * <p>{@code destroyMethod = "close"} is explicit for readability; Spring would
 * also auto-detect {@link AutoCloseable} on shutdown. Pools are therefore
 * disposed exactly once at JVM/context shutdown.
 */
@Configuration
public class OutboundHttpResourcesConfig {

    @Bean(destroyMethod = "close")
    ManagedHttpResources ngcHttpResources() {
        return OAuth2ClientUtils.getClientHttpConnectorManaged("ngc");
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources fndsHttpResources() {
        return OAuth2ClientUtils.getClientHttpConnectorManaged("fnds");
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources apiKeysHttpResources() {
        return OAuth2ClientUtils.getClientHttpConnectorManaged("api-keys");
    }
}
