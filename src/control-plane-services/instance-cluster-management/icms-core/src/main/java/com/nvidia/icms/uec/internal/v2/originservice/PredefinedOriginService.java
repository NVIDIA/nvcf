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
package com.nvidia.icms.uec.internal.v2.originservice;

import lombok.Getter;
import lombok.RequiredArgsConstructor;
import lombok.experimental.Delegate;

import java.util.Arrays;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;

@RequiredArgsConstructor
public enum PredefinedOriginService implements OriginService {
    GENERAL_ERROR(new OriginServiceValue(0x0), "GeneralError"),
    CASSANDRA(new OriginServiceValue(0x84), "CASSANDRA"),
    CLUSTER_AGENT(new OriginServiceValue(0x89), "CLUSTER_AGENT"),
    OAUTH(new OriginServiceValue(0x96), "OAUTH"),
    APIKEYS(new OriginServiceValue(0x97), "APIKEYS"),
    K8S(new OriginServiceValue(0xC2), "KubeAPI"),
    ICMS(new OriginServiceValue(0xE2), "ICMS"),
    NVCFAPI(new OriginServiceValue(0xE3), "NVCFAPI"),
    NVCA(new OriginServiceValue(0xE4), "NVCA"),
    NATS(new OriginServiceValue(0xE5), "NATS"),
    AWSSQS(new OriginServiceValue(0xE6), "AWSSQS")
    ;

    private static final Map<Integer, PredefinedOriginService> codeToValueMap = Arrays
            .stream(values())
            .collect(Collectors.toMap(PredefinedOriginService::code, value -> value));

    @Delegate
    private final OriginService delegate;

    @Getter
    private final String shortName;

    public static Optional<OriginService> from(int code) {
        return Optional.ofNullable(codeToValueMap.get(code));
    }

    @Override
    public Optional<String> humanReadableName() {
        return Optional.of(this.shortName);
    }
}
