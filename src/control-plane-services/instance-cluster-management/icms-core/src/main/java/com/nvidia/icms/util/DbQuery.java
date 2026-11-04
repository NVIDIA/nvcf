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
package com.nvidia.icms.util;

import java.util.List;
import java.util.Map;

public interface DbQuery<T, V> {

    /** Defines a task for executing a query with input parameter of type @T and result element of type @V.
     * Task supposed to be run in the separate thread, however resulting map @result and input list @parameters are shared between all threads.
     * Results are provided inside @result map.
     *
     * @param result provides a isolated list for result from this task in @result[queryIndex] element. @queryIndex is unique for all tasks.
     * @param parameters is a shared list of input parameter of type @T. @indexStart and @indexEnd define a range of parameters that has to be processed by this task.
     * @param queryIndex is unique index for input parameter in @parameters that has to be processed by this task. Also specifies a list for storing result in @result map
     * @param indexStart defines the first parameter in the @parameters list for that task
     * @param indexEnd defines the last parameter in the @parameters list for that task
     **/
    void executeQuery(Map<Integer, List<V>> result, List<T> parameters, int queryIndex, int indexStart, int indexEnd);
}
