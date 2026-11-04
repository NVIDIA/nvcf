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
package com.nvidia.icms.uec.internal.v2.reportingservice;

import lombok.RequiredArgsConstructor;
import lombok.experimental.Delegate;

import java.util.Arrays;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;


/**
 * Definitions are taken from
 * <a href="https://confluence.nvidia.com/pages/viewpage.action?spaceKey=GNCE&title=New+Schema+Format">...</a>.
 */
@RequiredArgsConstructor
public enum PredefinedReportingService implements ReportingService {
    UNDEFINED(new ReportingServiceValue(0x0)),
    CLUSTER_AGENT(new ReportingServiceValue(0x8)),
    ICMS(new ReportingServiceValue(0xB));

    @Delegate
    private final ReportingService delegate;

    private static final Map<Integer, PredefinedReportingService> codeToValueMap = Arrays
            .stream(values())
            .collect(Collectors.toMap(PredefinedReportingService::code, value -> value));

    public static Optional<ReportingService> from(int code) {
        return Optional.ofNullable(codeToValueMap.get(code));
    }

    @Override
    public Optional<String> humanReadableName() {
        return Optional.of(this.name());
    }
}
