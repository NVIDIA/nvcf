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

import com.nvidia.icms.uec.internal.v2.NumericalRepresentation;
import com.nvidia.icms.uec.internal.v2.UnifiedError;

/**
 * UEC Severity.
 * <p>
 * Predefined values available in {@link PredefinedSeverity}.
 */
@FunctionalInterface
public interface Severity extends NumericalRepresentation {

    int BITS = 4;

    static Severity from(SeverityBit... bits) {
        return from(SeverityValue.bitsToInt(bits));
    }

    static Severity from(int code) {
        return PredefinedSeverity.from(code)
                .orElseGet(() -> new SeverityValue(code));
    }

    /**
     * 4 bits that define severity of a {@link UnifiedError}.
     */
    @Override
    int code();

    default boolean isError() {
        return (code() & SeverityBit.ERROR.getBit()) != 0;
    }

    default boolean isUserVisible() {
        return (code() & SeverityBit.USER_VISIBLE.getBit()) != 0;
    }

    default boolean isUserInitiated() {
        return (code() & SeverityBit.USER_INITIATED.getBit()) != 0;
    }
}
