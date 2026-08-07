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
package com.nvidia.ess.repository;

import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_CREATE_TYPE_KEY;
import static com.nvidia.ess.util.RepositoryWriteOpsTestUtils.mockBatchStatementExecution;
import static com.nvidia.ess.util.RepositoryWriteOpsTestUtils.mockInsertStatementExecution;
import static com.nvidia.ess.util.RepositoryWriteOpsTestUtils.verifyReactiveWriteExecution;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_NEK_ID;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyIterable;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialCreateType;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.models.SecretVersionPartitionModel;
import com.nvidia.ess.persistence.repositories.SecretVersionRepositoryCustomWriteOpsImpl;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.util.CustomObjectMatchers;
import com.nvidia.ess.util.RepositoryWriteOpsTestUtils.StatementExecResult;
import java.time.Instant;
import java.util.Arrays;
import java.util.List;
import java.util.stream.Stream;
import org.apache.commons.lang3.tuple.Triple;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.core.ReactiveCassandraBatchOperations;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.data.cassandra.core.cql.WriteOptions;
import org.springframework.data.cassandra.core.query.Criteria;
import org.springframework.data.cassandra.core.query.Filter;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
public class SecretVersionRepositoryWriteOpsTest {

  @Mock
  private ReactiveCassandraBatchOperations batchOps;

  @Mock
  private ReactiveCassandraTemplate reactiveCassandraTemplate;

  @Mock
  private TelemetryComponents telemetryComponents;

  @InjectMocks
  private SecretVersionRepositoryCustomWriteOpsImpl repositoryWriteOps;

  private static Stream<Arguments> argsTestSaveNewSecretVersion() {
    // Form (isCasInsert[boolean], nonNullCurrentVersionAvailableForCas[boolean], execResult[enum]) tuples.
    return Stream.of(false, true).flatMap(isCasInsert ->
        Stream.of(false, true).flatMap(
            oldSecretVersionNotNull -> Arrays.asList(StatementExecResult.values()).stream().map(result ->
                Triple.of(isCasInsert, oldSecretVersionNotNull, result))
        )
    )
    // Discard tuples where isCasWrite == false && execResult == RESULT_FALSE
    // (for non-CAS, we only deal with `result is true` or `result.empty()` or `result.error()` situations).
    .filter(tuple -> tuple.getLeft() || !tuple.getRight().equals(StatementExecResult.RESULT_FALSE))
    // Also discard tuples where isCasWrite == false && nonNullCurrentVersionAvailableForCas == [one-of-the-2-values]
    // (For non-CAS, the previous value of `current_version` does not matter).
    .filter(tuple -> tuple.getLeft() || !tuple.getMiddle())
    // Turn the tuple instance into an `Arguments` instance.
    .map(tuple -> Arguments.of(tuple.getLeft(), tuple.getMiddle(), tuple.getRight()));
  }

  @ParameterizedTest
  @MethodSource("argsTestSaveNewSecretVersion")
  void testSaveNewVersionWithLWT(boolean isCasInsert, boolean nonNullCurrentVersionAvailableForCas, StatementExecResult execResult) {


    String value = "test_payload";
 
    var oldSecretVersionForCas = nonNullCurrentVersionAvailableForCas ? Uuids.timeBased() : null;
    var newSecretVersion = Uuids.timeBased();

    var secretVersionModel = SecretVersionModel.builder()
        .namespace(TEST_NAMESPACE)
        .entity(TEST_ENTITY)
        .secretPath(TEST_SECRET_PATH)
        .version(newSecretVersion)
        .currentVersion(newSecretVersion)
        .createdAt(Instant.now())
        .encryptedAt(Instant.now())
        .encryptedByKid(TEST_NEK_ID)
        .value(value)
        .build();

    if (isCasInsert) {
      doReturn(batchOps).when(reactiveCassandraTemplate).batchOps();
      doReturn(batchOps).when(batchOps).insert(any(SecretVersionModel.class));
      doReturn(batchOps).when(batchOps).update(anyIterable(), any(WriteOptions.class));
      mockBatchStatementExecution(batchOps, execResult);
    } else {
      mockInsertStatementExecution(reactiveCassandraTemplate, secretVersionModel, execResult);
    }

    var res = assertDoesNotThrow(() -> isCasInsert
        ? repositoryWriteOps.saveNewVersionWithLWT(secretVersionModel, oldSecretVersionForCas)
        : repositoryWriteOps.saveNewVersionWithoutLWT(secretVersionModel));
    verifyReactiveWriteExecution(res, execResult, RetryableException.class);

    if (isCasInsert) {
      var expectedUpdate = SecretVersionPartitionModel.builder()
          .namespace(TEST_NAMESPACE)
          .entity(TEST_ENTITY)
          .secretPath(TEST_SECRET_PATH)
          .currentVersion(newSecretVersion)
          .build();
      var expectedUpdateLWTCondition = Filter.from(
          Criteria.where(SecretVersionModel.COLUMN_CURRENT_VERSION).is(oldSecretVersionForCas)
      );
      verify(batchOps, times(1)).update(
              eq(List.of(expectedUpdate)),
              argThat(CustomObjectMatchers.updateOptionsIfConditionMatches(expectedUpdateLWTCondition))
      );
      verify(batchOps, times(1)).insert(eq(secretVersionModel));
      verify(batchOps, times(1)).execute();
      verify(reactiveCassandraTemplate, never()).insert(any());  

    } else {

      verify(reactiveCassandraTemplate, times(1)).insert(eq(secretVersionModel));
      verify(reactiveCassandraTemplate, never()).batchOps();
    }

    if (execResult == StatementExecResult.ERROR) {
      verify(telemetryComponents).setSpanAttribute(
              any(ContextView.class), eq(PARTIAL_CREATE_TYPE_KEY),
              eq(PartialCreateType.SECRET_VERSION_AFTER_PATH_UNKNOWN.name()));
      verifyNoMoreInteractions(telemetryComponents);
    } else {
      verifyNoInteractions(telemetryComponents);
    }
  }
}
