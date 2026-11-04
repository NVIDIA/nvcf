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
package com.nvidia.icms.uec.internal.util;

public class BitMaskUtil {

    public static int extractAllExceptFirst(int number, int offset) {
        return extractBitRange(number, 32 - offset, offset);
    }

    public static int extractBitRange(int number, int count, int offset) {
        if (count < 0 || count > 32) {
            throw new IllegalArgumentException("count must be between 0 and 32, got: " + count);
        }
        if (offset < 0 || offset > 31) {
            throw new IllegalArgumentException("offset must be between 0 and 31, got: " + offset);
        }
        if (count + offset > 32) {
            throw new IllegalArgumentException("count + offset must be <= 32, got: " + (count + offset));
        }
        if (count == 0) {
            return 0;
        }
        int mask = count == 32 ? -1 : (1 << count) - 1;
        return (number >>> offset) & mask;
    }
}
