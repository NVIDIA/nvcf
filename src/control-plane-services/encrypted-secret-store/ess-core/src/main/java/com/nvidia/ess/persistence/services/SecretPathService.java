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
package com.nvidia.ess.persistence.services;


import static com.nvidia.ess.constants.OpenTelemetryAttributes.LWT_WRITE_FAILURE_OPERATION_KEY;

import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.Errors;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.SubErrors;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedTooManyRequestsException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.repositories.SecretPathPartitionRepository;
import com.nvidia.ess.persistence.repositories.SecretPathRepository;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.LogMessageStringUtils;
import com.nvidia.ess.utils.SecretPathUtils;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.Optional;
import lombok.NonNull;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class SecretPathService {

    @Setter(onMethod_ = {@Autowired})
    private SecretPathRepository repository;

    @Setter(onMethod_ = {@Autowired})
    private SecretPathPartitionRepository partitionRepository;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    /**
     *
     * Prior to inserting a secret, perform writes to ensure that the secret-path and all its
     * ancestor-directory-paths exist
     *
     * @param namespace
     * @param entity
     * @param secretPath
     * @return
     */
    public Mono<SecretPathWriteArgs> writeAllPathsForSecret(@NonNull String namespace, @NonNull String entity,
            @NonNull String secretPath) {

        // First, attempt to separately fetch `entity_version` from the secret-paths table partition to which this
        // secret would belong (if it exists). This is done by fetching an arbitrary row in the partition and extracting
        // the `entity_version` column-value from that row. This will be the `entity_version` checked during CAS as
        // part of path-insertion (and setting a new `entity_version` value). `entity_version` should default to `NULL`
        // if the partition doesn't exist.
        //
        return Mono.deferContextual(ctx ->
            partitionRepository.findFirstByNamespaceAndEntity(namespace, entity)
                .onErrorMap(ex -> {
                    // Rethrow any DB-fetch errors as a `RetryableException` (if there's an outer-retry-loop with retries remaining,
                    // a retry of the entire execution-pipeline from the beginning is attempted. Otherwise, an internal server error
                    // is returned to the API caller).
                    return new RetryableException(new RetriesExhaustedInternalErrorException(
                        LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                            SubErrors.ENTITY_VERSION_PREFETCH_FAILURE, namespace, entity, secretPath),
                        "Unexpected error while prefetching entity_version " +
                            LogMessageStringUtils.namespaceEntitySecretTuple(namespace, entity, secretPath), ex));
                })
                .map(arbitraryPathInPartition -> Optional.of(arbitraryPathInPartition.getEntityVersion()))
                // If the fetch yielded an empty result, it likely means that the partition doesn't exist.
                // `entity_version` is therefore NULL. (NOTE: `Mono.just(null)` is not allowed, hence the use of an
                // `Optional<>` to hold the underlying UUID [whether it's null or otherwise])
                .switchIfEmpty(Mono.just(Optional.empty()))
                .flatMap(currentEntityVersionOpt -> {
                    var currentEntityVersion = currentEntityVersionOpt.orElse(null);
                    // Next, get all the path-prefixes of this secret, including the secret itself.
                    var allPathPrefixes = SecretPathUtils.getAllSecretPathPrefixes(secretPath);

                    // Of those path-prefixes, find all those that already exist in the table (fetch their `SecretPathModel`
                    // row-entities).
                    var existingPathPrefixes = repository.findAllByNamespaceAndEntityAndPathIn(
                                    namespace, entity, allPathPrefixes.keySet().stream().toList()
                            )
                            .onErrorMap(ex -> {
                                // Rethrow any DB-fetch errors as a `RetryableException`.
                                return new RetryableException(new RetriesExhaustedInternalErrorException(
                                    LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                                        SubErrors.EXISTING_PATH_FETCH_FAILURE, namespace, entity, secretPath),
                                    "Unexpected error while fetching existing secret-paths to check for conflicts " +
                                        LogMessageStringUtils.namespaceEntitySecretTuple(namespace, entity, secretPath), ex));
                            });

                    // Validate all the path-prefixes that already exist in the table (the secret-path, if it exists in the table,
                    // shouldn't be marked as a directory, and none of its [other] path-prefixes, if any exist in the table, should
                    // be marked as a secret-path. Construct `SecretPathModel` instances for the path-prefixes that couldn't be
                    // found. These paths will have to be inserted.
                    //
                    // In addition, if any of the paths did exist already, use the `entity_version` value obtained from this
                    // fetch (ignore the value fetched earlier) for the CAS operation that will be part of path-insertion.
                    // Generate the new value of `entity_version` to use as part of CAS as well.
                    //
                    var insertionArgs = SecretPathUtils.validateAndGetInsertionArgs(namespace, entity, existingPathPrefixes, allPathPrefixes);

                    return insertionArgs.map(args -> {
                        if (args.getPrevEntityVersionForCAS() == null) {

                            // If none of the path-prefixes already exist in the table, an `entity_version` wouldn't have
                            // been obtained from the corresponding fetch. Use the `entity_version` from the earlier fetch for use
                            // as the CAS-check.
                            //
                            return args.toBuilder()
                                    .prevEntityVersionForCAS(currentEntityVersion)
                                    .build();
                        }
                        return args;
                    });
                })
                .flatMap(args -> {

                    // Perform insertion of the paths that needed to be inserted.
                    return repository.batchWriteSecretPathsWithEntityVersionLWT(namespace, entity, args)
                            // The repository batch-write must return a non-empty Mono (a failed operation returns a
                            // Mono.error(RetryableException) while a finished operation returns a Mono.just(Boolean)).
                            .flatMap(insertionSuccessful -> {
                                if (!insertionSuccessful) {
                                    // A write needed to happen but it didn't succeed (even though an explicit error wasn't thrown).
                                    // This blocks downstream operations and the execution-pipeline needs to stop here (it can be
                                    // retried).
                                    telemetryComponents.setSpanAttribute(ctx,
                                            LWT_WRITE_FAILURE_OPERATION_KEY,
                                            LwtOperation.PATH_CREATION.name());
                                    customMetricsRegistry.recordRetryableLwtFailure(namespace,
                                            LwtOperation.PATH_CREATION);

                                    // This error should be retryable if there's an outer-retry loop but if
                                    // retries are exhausted, a 429 status-code should be returned.
                                    return Mono.error(new RetryableException(
                                        new RetriesExhaustedTooManyRequestsException(
                                            LogMessageStringUtils.errorSummary(Errors.TOO_MANY_REQUESTS,
                                                SubErrors.TOO_MANY_REQUESTS_ON_ENTITY, namespace, entity),
                                            String.format(
                                                "Too many simultaneous writes to %s. Please slow down " +
                                                "secret-insertions to this entity.",
                                                LogMessageStringUtils.namespaceEntityTuple(namespace, entity)))
                                    ));
                                }
                                // Echo `SecretPathWriteArgs` to downstream if the insertion was successful (or there
                                // was nothing to be done).
                                return Mono.just(args);
                            });
                })
        );
    }

    public Flux<SecretPathModel> getPaths(String namespace, String entity) {
        return repository.findAllByNamespaceAndEntity(namespace, entity);
    }

    public Mono<Boolean> deleteSecretPathAndEmptyAncestorDirs(@NonNull String namespace, @NonNull String entity,
            @NonNull String path) {
        log.debug("deleting secret-path and cleaning up empty ancestor-directory-paths: namespace: {} entity: {} " +
                "path: {}", namespace, entity, path);
        return SecretPathUtils.getSecretPathAndEmptyDirectoriesToDelete(
                // Fetch the list of paths in the entity.
                //
                // Then, from the fetched path-list, and given the secret-path-to-be-deleted, determine the
                // list of paths to delete for this operation (if secret-path-to-be-deleted exists in the DB)
                // which should consist of the secret-path itself as well as any ancestor directory-paths in
                // the DB that would be rendered empty upon its deletion.
                repository.findAllByNamespaceAndEntity(namespace, entity), path
        )
            .flatMap(deletionArgs -> {
                // If a non-empty list of paths was found for deletion, delete those paths with an LWT-guarded
                // batch-transaction.
                return repository.deletePathsByVersion(
                        namespace,
                        entity,
                        deletionArgs.getPathsToWrite()
                                .stream()
                                .map(SecretPathModel::getPath)
                                .toList(),
                        deletionArgs.getPrevEntityVersionForCAS(),
                        deletionArgs.getNewEntityVersion()
                );
            })
            // If the paths-in-entity fetch or the form-paths-to-delete operations above returned
            // an empty list of paths, then nothing needs to be done. Return a noop success.
            .defaultIfEmpty(true)
            // If any of the above operations above returned an error, ensure to wrap it inside
            // a `RetryableException` (retryable if an outer retry-loop exists and has retries left, 500
            // otherwise).
            .onErrorMap(
                ex -> !(ex instanceof BootResponseException),
                ex -> new RetryableException(new RetriesExhaustedInternalErrorException(
                          LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                              SubErrors.UNHANDLED_PATH_DELETION_FAILURE, namespace, entity, path),
                          "unexpected error during secret-path deletion", ex))
            );
    }

    // Note: this function always returns true. The caller should ignore the value.
    // The underlying repository call returns an empty mono, but this function returns a boolean
    // mono to avoid propagating emptiness and to help with chaining subsequent reactive calls.
    public Mono<Boolean> deleteAll(String namespace, String entity) {
        return repository.deleteByNamespaceAndEntity(namespace, entity)
                .thenReturn(true);
    }
}
