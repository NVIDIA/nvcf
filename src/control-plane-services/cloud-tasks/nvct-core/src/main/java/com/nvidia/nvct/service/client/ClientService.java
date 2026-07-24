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
package com.nvidia.nvct.service.client;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvct.service.client.dto.ClientDto;
import com.nvidia.nvct.service.nvcf.NvcfClient;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ClientService {
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";

    private final NvcfClient nvcfClient;

    public ClientDto getClient(String clientId) {
        if (StringUtils.isBlank(clientId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("clientId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return nvcfClient.getClient(clientId);
    }

    @VisibleForTesting
    public void invalidateCache() {
        nvcfClient.invalidateCache();
    }
}
