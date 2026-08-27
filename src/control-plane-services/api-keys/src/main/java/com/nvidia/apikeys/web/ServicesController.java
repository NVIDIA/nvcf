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

import com.nvidia.apikeys.dto.keys.ListServicesResponse;
import com.nvidia.apikeys.dto.services.ServiceDto;
import com.nvidia.apikeys.facade.ServiceOperationsFacade;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import lombok.RequiredArgsConstructor;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
public class ServicesController {

    public static final String SERVICE_ID = "service-id";

    private final ServiceOperationsFacade facade;

    @Operation(summary = "Reads service", description = "Reads service details.")
    @GetMapping("/v1/services/{service-id}")
    public ServiceDto getService(
            @Parameter(name = SERVICE_ID, required = true, description = "Unique identifier of service")
            @PathVariable(SERVICE_ID) String serviceId) {
        return facade.getService(serviceId);
    }

    @Operation(summary = "List services", description = "Returns list of service configurations")
    @GetMapping("/v1/services")
    public ListServicesResponse listServices() {
        return facade.listServices();
    }
}
