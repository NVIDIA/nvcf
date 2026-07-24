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
package com.nvidia.nvct.service.nvcf;

import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.client.dto.ClientDto;
import lombok.Builder;
import lombok.extern.jackson.Jacksonized;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.service.annotation.GetExchange;

public interface NvcfStubService {

    @GetExchange("/v2/nvcf/accounts/{ncaId}")
    NvcfAccountResponse fetchAccount(@PathVariable String ncaId);

    @GetExchange("/v2/nvcf/clients/{clientId}")
    NvcfClientResponse fetchClient(@PathVariable String clientId);

    @lombok.Value
    @Jacksonized
    @Builder
    class NvcfAccountResponse {
        AccountDto account;
    }

    @lombok.Value
    @Jacksonized
    @Builder
    class NvcfClientResponse {
        ClientDto client;
    }
}
