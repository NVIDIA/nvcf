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
package com.nvidia.nvct.service.reval;

import com.fasterxml.jackson.annotation.JsonInclude;
import tools.jackson.databind.node.ObjectNode;
import java.util.List;
import lombok.Builder;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.service.annotation.PostExchange;

public interface RevalStubService {

    @Value
    @Jacksonized
    @Builder
    @JsonInclude(JsonInclude.Include.NON_NULL)
    class RevalValidateRequest {
        String helmChart;

        String instanceType;

        String gpu;

        ObjectNode configuration;

        ObjectNode imageRegistryAuthConfig;

        ObjectNode helmRegistryAuthConfig;
    }

    @Value
    @Jacksonized
    @Builder
    class RevalValidateResponse {
        boolean valid;

        List<String> validationErrors;

        List<String> internalErrors;
    }

    @PostExchange("/v1/validate")
    ResponseEntity<RevalValidateResponse> validate(@RequestBody RevalValidateRequest request);
}
