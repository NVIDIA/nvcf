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
package com.nvidia.nvcf.configuration.nats;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import io.nats.client.NKey;
import java.net.URI;
import java.util.Optional;
import org.junit.jupiter.api.Test;

class NatsConfigurationTest {

    @Test
    void reconnectsWithoutLimit() throws Exception {
        var natsProperties = new NatsProperties();
        natsProperties.setUrl(URI.create("nats://localhost:4222"));
        natsProperties.setApiUserSeed(new String(NKey.createUser(null).getSeed()));

        var options = NatsConfiguration.options(natsProperties, Optional.empty());

        assertThat(options.getMaxReconnect()).isEqualTo(-1);
    }
}
