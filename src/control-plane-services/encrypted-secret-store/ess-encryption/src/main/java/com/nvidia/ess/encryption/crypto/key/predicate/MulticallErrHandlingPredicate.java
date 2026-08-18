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
package com.nvidia.ess.encryption.crypto.key.predicate;

import java.util.Objects;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Consumer;
import java.util.function.Predicate;
import org.apache.commons.lang3.function.TriFunction;

/**
 * 
 * <p>A {@link Predicate}-like interface that contains an {@link ErrorReportingPredicate} and is able to hold the
 * {@link E} that was the result of cumulative calls to an error-handling reducer:
 * {@link MulticallErrHandlingPredicate#reducerErrHandler} (signature:
 * {@code (PredicateError prev_held_err_object, PredicateError curr_err_object, TestedObject obj) -> new_err_obj })
 * across all errors forwarded from multiple underlying calls of {@link ErrorReportingPredicate#test(Object, Consumer)}
 * invoked via {@link MulticallErrHandlingPredicate#test(Object, boolean)}.</p>
 * 
 * <p>In essence, the {@link MulticallErrHandlingPredicate#reducerErrHandler} is able to perform error-handling across multiple
 * failed invocations of a given predicate and return a single error-object to the caller representative of all errors,
 * without interrupting the business-logic that invokes the predicate, or burdening the business-logic with additional
 *  complexity for error-handling purposes.</p>
 * 
 * <p>The single {@link E} error-object accumulated across all calls to the underlying predicate can be accessed
 * using {@link MulticallErrHandlingPredicate#cumulativeError()}.</p>
 * 
 * <p>This is useful when multiple calls to a predicate to validate multiple candidates need to
 * have their failure-reasons handled after iteration of all candidates from a reactive-stream
 * has concluded, instead of interrupting that stream each time a failure is encountered (resumption may not be
 * possible as candidate-iteration may not be paged) or accumulating failure-reasons using a reduce-operation across
 * candidates in a subscriber to that reactive-stream which can complicate the code used to implement
 * business-logic.</p>
 * 
 */
public final class MulticallErrHandlingPredicate<T, E> {

    /**
     * <p>Similar to a {@link Predicate} interface but also provides for a {@link Consumer} from
     * which instances of error-objects (type {@link E}) can be forwarded to the
     * caller for more granular error-handling that cannot be done within the predicate itself.</p>
     */
    @FunctionalInterface
    public static interface ErrorReportingPredicate<T, E> {

        public boolean test(T t, Consumer<E> errConsumer);
    }

    private final ErrorReportingPredicate<T, E> predicate;

    // (prev_held_error_obj, current_reported_error, current_tested_object) -> error_obj_to_hold
    private final TriFunction<E, E, T, E> reducerErrHandler;

    private final AtomicReference<E> holder = new AtomicReference<>();

    /**
     * 
     * <p>Returns the {@link E} that was determined by cumulative error-handling
     * across all calls to {@link MulticallErrHandlingPredicate#test(Object, boolean)} on the same instance
     * of {@link MulticallErrHandlingPredicate}, via the error-handler:
     * {@link MulticallErrHandlingPredicate#reducerErrHandler}.</p>
     * 
     * @return
     */
    public E cumulativeError() {
        return holder.get();
    }

    /**
     * 
     * <p>Similar to {@link Predicate#test(Object)} - invokes the underlying {@link MulticallErrHandlingPredicate#predicate}
     * and returns the predicate-result while also applying the error-handling reducer 
     * {@link MulticallErrHandlingPredicate#reducerErrHandler} to any {@link E} reported by the call to
     * determine the new cumulative result of error-handling across all calls to this predicate, that is accessible via
     * {@link MulticallErrHandlingPredicate#cumulativeError()}.</p>
     * 
     * <p>The second argument {@param toggleErrorReporting} can be used by the direct caller of the predicate
     * (e.g. a cache-loader) to enable error-reporting inside the underlying predicate or suppress it.</p>
     * 
     * @param t
     * @param toggleErrorReporting
     * @return
     */
    public boolean test(T t, boolean toggleErrorReporting) {
        return predicate.test(t, error -> {
            if (toggleErrorReporting && !Objects.isNull(error)) {
                holder.updateAndGet(lastError -> reducerErrHandler.apply(lastError, error, t));
            }
        });
    }

    private MulticallErrHandlingPredicate(ErrorReportingPredicate<T, E> p,
            TriFunction<E, E, T, E> reducer) {
        this.predicate = p;
        this.reducerErrHandler = reducer;
    }

    /**
     * 
     * Factory for instances of {@link MulticallErrHandlingPredicate} that takes an {@link ErrorReportingPredicate}
     * predicate {@code p} as well as a reducer error-handler {@code r}.
     * <p>
     * WARNING: Reducer {@code r} MUST be idempotent and safe to invoke more than once
     *
     * @param <T>
     * @param <E>
     * @param p
     * @param r
     * @return
     */
    public static <T, E> MulticallErrHandlingPredicate<T, E> create(ErrorReportingPredicate<T, E> p,
            TriFunction<E, E, T, E> r) {
        return new MulticallErrHandlingPredicate<>(p, r);
    }

    /**
     * 
     * Factory for instances of {@link MulticallErrHandlingPredicate} that takes an {@link ErrorReportingPredicate}
     * predicate {@code p}, with the default reducer error-handler which simply preserves the most recently reported
     * {@link E} to be accessed by a call to {@link MulticallErrHandlingPredicate#cumulativeError()}
     *
     * @param <T>
     * @param <E>
     * @param p
     * @return
     */
    public static <T, E> MulticallErrHandlingPredicate<T, E> create(ErrorReportingPredicate<T, E> p) {
        return new MulticallErrHandlingPredicate<>(p, (e1, e2, o) -> e2);
    }
}
