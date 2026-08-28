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
package com.nvidia.icms.configuration.nats;

import java.time.Duration;
import java.util.Optional;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "icms.nats")
@Data
@Slf4j
public class NatsConfigurationProperties {

    private boolean natsEnabled;
    private String natsUrl;
    private int maxPoolSize = 8;
    private boolean createNatsStreams;
    private Duration connectionTimeout = Duration.ZERO;
    private Duration pingInterval = Duration.ZERO;
    private Duration reconnectWait = Duration.ZERO;
    private Duration reconnectJitter = Duration.ZERO;
    private boolean reconnectAllowed;
    private Duration forceReconnectFlush = Duration.ZERO;
    private Duration delayBetweenMessages = Duration.ZERO;
    private Duration messageTtl = Duration.ZERO;
    private Duration globalStreamValidationTaskLockTtl = Duration.ZERO;
    private boolean globalStreamValidationTaskEnabled;
    private Optional<String> nkeySeed = Optional.empty();
}
