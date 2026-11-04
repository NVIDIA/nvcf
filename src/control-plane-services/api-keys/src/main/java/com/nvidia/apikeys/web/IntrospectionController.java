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

package com.nvidia.apikeys.web;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import com.nvidia.apikeys.facade.IntrospectionFacade;
import io.swagger.v3.oas.annotations.Operation;
import java.util.concurrent.TimeUnit;
import lombok.RequiredArgsConstructor;
import org.springframework.http.CacheControl;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE, consumes = APPLICATION_JSON_VALUE)
public class IntrospectionController {

    private final IntrospectionFacade facade;

    @Operation(summary = "Introspects api key", description = "Introspects api key.")
    @PostMapping("/v1/introspect")
    @ResponseStatus(HttpStatus.OK)
    public ResponseEntity<IntrospectionResponse> introspectKey(
            @RequestBody IntrospectionRequest request) {

        return ResponseEntity.ok()
                .cacheControl(CacheControl.maxAge(360, TimeUnit.SECONDS))
                .body(facade.introspect(request));
    }

}
