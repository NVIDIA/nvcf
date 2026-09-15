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

import com.nvidia.icms.uec.internal.util.BitMaskUtil;

record OriginServiceValue(int code) implements OriginService {

    OriginServiceValue {
        var unusedBits = BitMaskUtil.extractAllExceptFirst(code, BITS);
        if (unusedBits != 0) {
            var msg = "Only [%d] bits can be used to define an origin service.".formatted(BITS);
            throw new IllegalArgumentException(msg);
        }
    }
}
