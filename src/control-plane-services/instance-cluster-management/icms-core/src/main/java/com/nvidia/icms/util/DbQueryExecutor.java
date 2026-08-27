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

import com.nvidia.icms.errors.IcmsInternalServerException;
import java.lang.management.ManagementFactory;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@AllArgsConstructor
public class DbQueryExecutor<T, V> {

    private final DbQueryExecutorService dbQueryExecutorService;

    /**
     * Executes task @query in multi-thread way.
     * These tasks share the list of input parameters @parameters of type @T
     * and return their own resulting  list of type @V.
     * The returned results are stored in an internal map where key is a task index and a value is a resulting list from that task.
     *
     * @param parameters provides a list of input parameters for tasks.
     * @param minParametersPerThread specifies a number of parameters for a single task that are processed one-by-on inside the single thread.
     * @param maxThreadsInParallel specifies a number of threads that can be executed in parallel. It splits all threads into cycles and the next cycle starts when all threads from previous one finish.
     * @param query provides a runnable object that has to be executed in teh thread.
     * @return a resulting list of elements of type @V collected from all threads.
     * @throws RuntimeException if issues happened with a thread.
     **/
    public List<V> executeQueries(List<T> parameters, int minParametersPerThread, int maxThreadsInParallel, DbQuery<T, V> query, String queryName) {
        List<V> result = new ArrayList<>();

        if (parameters == null || parameters.isEmpty()) {
            return result;
        }

        int maxThreadNumber = parameters.size() / minParametersPerThread + (
                parameters.size() % minParametersPerThread > 0 ? 1 : 0);
        log.debug(
                "executeQueries queryName {}. max number of parameters {}, parametersPerThread {}  maxThreadNumber {}, maxThreadsInParallel {}",
                queryName,
                parameters.size(),
                minParametersPerThread,
                maxThreadNumber,
                maxThreadsInParallel);

        Map<Integer, List<V>> resultByParameterIndex = new ConcurrentHashMap<>();

        int needThreadsInParallel = Math.min(maxThreadsInParallel, maxThreadNumber);
        int numberOfParametersPerThread = parameters.size() / needThreadsInParallel;

        Map<Integer, CompletableFuture<?>> tasks = new HashMap<>();

        // Create all runnables
        for (int threadIndex = 0; threadIndex < needThreadsInParallel; threadIndex++) {
            int threadIndexLocal = threadIndex;
            int startIndex = threadIndexLocal * numberOfParametersPerThread;
            int endIndex; // excluded

            if (needThreadsInParallel == threadIndex + 1) { // last thread
                endIndex = parameters.size();
            } else {
                endIndex = startIndex + numberOfParametersPerThread;
            }

            String name = String.format(
                    "dbquery_%s_thread_%d_indextstart_%d_indexend_%d",
                    queryName,
                    threadIndex,
                    startIndex,
                    endIndex);

            CompletableFuture<?> feature = CompletableFuture.runAsync(
                    () -> query.executeQuery(resultByParameterIndex,
                                             parameters,
                                             threadIndexLocal,
                                             startIndex,
                                             endIndex),
                    dbQueryExecutorService.getExecutor());

            log.trace(
                    "queryName {}: Created runnable for threadIndex %d parameter start %d end %d  name %s%n",
                    queryName,
                    threadIndex,
                    startIndex,
                    endIndex,
                    name);

            tasks.put(threadIndex, feature);
        }

        //Wait for all threads
        tasks.forEach((k, v) -> {
            try {
                if (v != null) {
                    log.trace("Getting values from thread  {}...", k);
                    v.get();
                    log.trace("Got values from thread  {}...", k);
                    Thread.yield();
                }
            } catch (InterruptedException | ExecutionException e) {
                log.error("queryName {}: Interrupted Error executing db query {}: {}", queryName, k,
                          e.getMessage());
                throw new RuntimeException(e);
            }
        });

        resultByParameterIndex.values().forEach(result::addAll);

        return result;
    }


}
