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

import com.nvidia.icms.uec.internal.v2.NumericalRepresentation;
import com.nvidia.icms.uec.internal.v2.UnifiedError;

import java.util.Optional;

/**
 * UEC Origin Service.
 * <p>
 * Predefined values available in {@link PredefinedOriginService}.
 */
public interface OriginService extends NumericalRepresentation {

    int BITS = 8;

    static OriginService from(int code) {
        return PredefinedOriginService.from(code)
                .orElseGet(() -> new OriginServiceValue(code));
    }

    /**
     * 8 bits that define origin service of {@link UnifiedError}.
     */
    int code();


    /**
     * @return human-readable name representation if it is known.
     */
    default Optional<String> humanReadableName() {
        return Optional.empty();
    }
}
