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
package com.nvidia.icms.uec.internal.v2.severity;

import lombok.RequiredArgsConstructor;
import lombok.experimental.Delegate;

import java.util.Arrays;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;

/**
 * All possible combinations of {@link SeverityBit} excluding {@link SeverityBit#RESERVED}.
 */
@RequiredArgsConstructor
public enum PredefinedSeverity implements Severity {
    WARNING(new SeverityValue()),
    WARNING_USER_VISIBLE(new SeverityValue(SeverityBit.USER_VISIBLE)),
    WARNING_USER_INITIATED(new SeverityValue(SeverityBit.USER_INITIATED)),
    WARNING_USER_VISIBLE_USER_INITIATED(new SeverityValue(SeverityBit.USER_VISIBLE, SeverityBit.USER_INITIATED)),
    ERROR(new SeverityValue(SeverityBit.ERROR)),
    ERROR_USER_VISIBLE(new SeverityValue(SeverityBit.ERROR, SeverityBit.USER_VISIBLE)),
    ERROR_USER_INITIATED(new SeverityValue(SeverityBit.ERROR, SeverityBit.USER_INITIATED)),
    ERROR_USER_VISIBLE_USER_INITIATED(new SeverityValue(SeverityBit.ERROR, SeverityBit.USER_VISIBLE, SeverityBit.USER_INITIATED));

    private static final Map<Integer, PredefinedSeverity> codeToValueMap = Arrays
            .stream(values())
            .collect(Collectors.toMap(PredefinedSeverity::code, value -> value));

    @Delegate
    private final Severity delegate;

    public static Optional<Severity> from(int code) {
        return Optional.ofNullable(codeToValueMap.get(code));
    }
}
