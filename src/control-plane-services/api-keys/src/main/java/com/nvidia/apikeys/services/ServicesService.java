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

package com.nvidia.apikeys.services;

import tools.jackson.core.JacksonException;
import tools.jackson.core.type.TypeReference;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
public class ServicesService {

    private final Map<String, ServiceVo> services;

    public ServicesService(NakProperties config)
            throws JacksonException {
        log.info("Initializing services {}", config.getRegistrations());
        List<ServiceVo> registrations = JsonUtils.getRequestResponseJsonMapper().readValue(
                config.getRegistrations(), new TypeReference<>() {
                });

        this.services = registrations.stream()
                .collect(Collectors.toUnmodifiableMap(ServiceVo::getServiceId, s -> s));

        log.debug("Registered services: {}", this.services.values());
    }

    public ServiceVo loadById(String serviceId) {
        return getServiceById(serviceId)
                .orElseThrow(() -> new NotFoundException(
                        String.format("Service '%s' not found", serviceId)));
    }

    public Optional<ServiceVo> getServiceById(String serviceId) {
        return Optional.ofNullable(services.get(serviceId));
    }

    public List<ServiceVo> listServices() {
        return List.copyOf(services.values());
    }
}
