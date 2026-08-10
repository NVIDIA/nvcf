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
package com.nvidia.ess.util;

import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;

import com.datastax.oss.driver.api.core.cql.ResultSet;
import org.mockito.Mockito;
import org.springframework.data.cassandra.core.ReactiveCassandraBatchOperations;
import org.springframework.data.cassandra.core.ReactiveCassandraOperations;
import org.springframework.data.cassandra.core.WriteResult;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

public class RepositoryWriteOpsTestUtils {

  // TODO: `EMPTY` can probably be removed.
  //
  //  `ReactiveCassandraTemplate.[insert|delete|update]()` and `ReactiveCassandraTemplate.batchOps()./*..*/.execute()`
  // both don't seem to ever return a `Mono.empty()`. They seem to either return a `Mono.just(<Boolean>)` or
  // a `Mono.error(...)`.
  public static enum StatementExecResult {
    ERROR, EMPTY, RESULT_TRUE, RESULT_FALSE
  }

  public static void mockBatchStatementExecution(ReactiveCassandraBatchOperations batchOps,
      StatementExecResult execResult) {
    
    ResultSet mockResultSet;

    switch (execResult) {
      case ERROR:
        doReturn(Mono.error(new RuntimeException("write error"))).when(batchOps).execute();
        break;
      case EMPTY:
        doReturn(Mono.empty()).when(batchOps).execute();
        break;
      case RESULT_FALSE:
        mockResultSet = Mockito.mock(ResultSet.class);
        doReturn(false).when(mockResultSet).wasApplied();
        doReturn(Mono.just(WriteResult.of(mockResultSet))).when(batchOps).execute();
        break;
      default:
        mockResultSet = Mockito.mock(ResultSet.class);
        doReturn(true).when(mockResultSet).wasApplied();
        doReturn(Mono.just(WriteResult.of(mockResultSet))).when(batchOps).execute();
    }
  }

  public static <T> void mockInsertStatementExecution(ReactiveCassandraOperations ops, T entity, StatementExecResult execResult) {
    switch (execResult) {
      case ERROR:
        doReturn(Mono.error(new RuntimeException("write error"))).when(ops).insert(eq(entity));
        break;
      case EMPTY:
        doReturn(Mono.empty()).when(ops).insert(eq(entity));
        break;
      default:
        doReturn(Mono.just(entity)).when(ops).insert(eq(entity));
    }
  }

  public static void verifyReactiveWriteExecution(Mono<Boolean> res, StatementExecResult execResult,
      Class<? extends Exception> exceptionClassIfError) {
    var verifier = StepVerifier.create(res);
    switch (execResult) {
      case ERROR:
        verifier.expectError(exceptionClassIfError).verify();
        break;
      case EMPTY:
        verifier.expectNextCount(0).expectComplete().verify();
        break;
      case RESULT_FALSE:
        verifier.expectNext(false).expectComplete().verify();
        break;
      default:
        verifier.expectNext(true).expectComplete().verify();
    }
  }
}
