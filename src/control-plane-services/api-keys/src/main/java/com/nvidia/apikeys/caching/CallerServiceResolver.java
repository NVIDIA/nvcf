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

package com.nvidia.apikeys.caching;


import com.nvidia.apikeys.services.ServicesService;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;

@Service
@RequiredArgsConstructor
public class CallerServiceResolver {
    public static final String ERROR_CALLER_SERVICE_NOT_REGISTERED = "Caller service not registered";

    private static final String KEY_ISSUER_ID = "Key-Issuer-Id";
    private final ServicesService servicesService;

    public ServiceVo getCallerService() {
        String keyIssuerId = Optional.ofNullable(RequestContextHolder.getRequestAttributes())
                .map(ServletRequestAttributes.class::cast)
                .map(ServletRequestAttributes::getRequest)
                .map(request -> request.getHeader(KEY_ISSUER_ID))
                .stream()
                .findFirst()
                .orElseThrow(() -> new BadRequestException("Key-Issuer-Id is not set"));

        return servicesService.getServiceById(keyIssuerId)
                .orElseThrow(() -> new BadRequestException(ERROR_CALLER_SERVICE_NOT_REGISTERED));
    }

}
