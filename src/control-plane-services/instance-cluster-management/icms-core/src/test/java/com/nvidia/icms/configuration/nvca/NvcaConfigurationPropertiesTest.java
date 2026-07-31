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
package com.nvidia.icms.configuration.nvca;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.integration.IntegrationTest;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;


class NvcaConfigurationPropertiesTest extends IntegrationTest {

    @Autowired
    NvcaConfigurationProperties nvcaConfigurationProperties;

    @Test
    void nvcaConfigurationProperties_returnsSuccess() {

        assertEquals("q_gdn_spot_byoc_%s_%s.fifo",
                     nvcaConfigurationProperties.getCreationQueueNameFormat());
        assertEquals("q_gdn_spot_byoc_%s.fifo",
                     nvcaConfigurationProperties.getTerminationQueueNameFormat());
        assertEquals(
                "http://localhost:9092/.well-known/jwks.json",
                nvcaConfigurationProperties.getSisConfig().getPublicKeySetEndpoint());
        assertEquals("http://localhost:9092/token",
                     nvcaConfigurationProperties.getSisConfig().getTokenUrl());
        assertEquals("http://localhost:8080",
                     nvcaConfigurationProperties.getSisConfig().getSpotServiceUrl());
        assertEquals("http://localhost:8200",
                     nvcaConfigurationProperties.getVaultConfig().getAddress());
        assertTrue(nvcaConfigurationProperties.isOidcClusterIdentityEnabled());
    }
}
