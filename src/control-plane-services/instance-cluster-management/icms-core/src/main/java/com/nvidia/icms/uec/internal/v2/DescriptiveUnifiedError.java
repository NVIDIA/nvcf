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
package com.nvidia.icms.uec.internal.v2;

import lombok.NonNull;

/**
 * {@link UnifiedError} with additional name and description fields.
 */
public interface DescriptiveUnifiedError extends UnifiedError {

    static DescriptiveUnifiedError from(
            @NonNull String errorName,
            @NonNull String errorDescription,
            @NonNull UnifiedError unifiedError) {
        return new DescriptiveUnifiedErrorValue(errorName, errorDescription, unifiedError);
    }

    String errorName();

    String errorDescription();
}
