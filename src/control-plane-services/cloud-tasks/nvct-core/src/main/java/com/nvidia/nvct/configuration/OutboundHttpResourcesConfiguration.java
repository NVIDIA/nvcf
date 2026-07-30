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
package com.nvidia.nvct.configuration;

import com.nvidia.nvct.service.ess.EssClient;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.ngc.NgcRegistryClient;
import com.nvidia.nvct.service.nvcf.NvcfClient;
import com.nvidia.nvct.service.reval.RevalClient;
import com.nvidia.nvct.service.token.client.NotaryClient;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Creates singleton {@link ManagedHttpResources} beans for each outbound client.
 * These pools survive {@link org.springframework.cloud.context.config.annotation.RefreshScope
 * @RefreshScope} refreshes — only the {@code WebClient} is rebuilt when config changes.
 * Spring disposes them on application shutdown via {@code destroyMethod = "close"}.
 */
@Configuration
class OutboundHttpResourcesConfiguration {

    @Bean(destroyMethod = "close")
    ManagedHttpResources essHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(EssClient.CLIENT_REGISTRATION_ID);
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources ngcRegistryHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(NgcRegistryClient.CLIENT_REGISTRATION_ID);
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources nvcfHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(NvcfClient.CLIENT_REGISTRATION_ID);
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources revalHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(RevalClient.CLIENT_REGISTRATION_ID);
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources icmsHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(IcmsClient.CLIENT_REGISTRATION_ID);
    }

    @Bean(destroyMethod = "close")
    ManagedHttpResources notaryHttpResources() {
        return NvctOAuth2ClientUtils
                .getClientHttpConnectorManaged(NotaryClient.CLIENT_REGISTRATION_ID);
    }
}
