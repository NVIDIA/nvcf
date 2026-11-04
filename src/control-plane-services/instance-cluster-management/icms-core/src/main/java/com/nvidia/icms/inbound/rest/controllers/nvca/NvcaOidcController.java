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
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeResponse;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@Tag(name = "NVIDIA Cluster Agent OIDC")
@RequestMapping(path = "/v1/si/oidc", produces = APPLICATION_JSON_VALUE)
public class NvcaOidcController {

    private final NvcaController nvcaController;

    @PostMapping({"tokens/introspect", "token/introspect"})
    public ResponseEntity<TokenIntrospectResponse> introspectToken(
            @Valid @RequestBody TokenIntrospectRequest request) {
        return nvcaController.introspectToken(request);
    }

    @PostMapping({"nats-authorize", "natsAuthorize"})
    public ResponseEntity<NatsAuthorizeResponse> natsAuthorize(
            @Valid @RequestBody NatsAuthorizeRequest request) {
        return nvcaController.natsAuthorize(request);
    }
}
