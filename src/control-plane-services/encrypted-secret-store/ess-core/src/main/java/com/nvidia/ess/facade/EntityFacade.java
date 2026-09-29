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
package com.nvidia.ess.facade;

import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_DELETE_TYPE_KEY;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialDeleteType;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.persistence.services.SecretVersionService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.EntityUtils;
import com.nvidia.ess.utils.ExceptionUtils;
import java.util.Objects;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Service
@Slf4j
public class EntityFacade {

    @Setter(onMethod_ = {@Autowired})
    private SecretVersionService secretVersionService;

    @Setter(onMethod_ = {@Autowired})
    private SecretPathService secretPathService;

    @Setter(onMethod_ = {@Autowired})
    private EntityService entityService;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceService namespaceService;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    // Delete Entity using bottom up model.
    // SecretVersion -->  SecretPath --> Entity
    // Inserting order should be reversed top down.
    // Abort whole deletion if fail in SecretVersion and Entity, but tolerate failure deletion in SecretPath.
    public Mono<Void> deleteEntity(String namespace, String entityType, String entityId) {
        String entity = EntityUtils.getEntity(entityType, entityId);

        return Mono.deferContextual(ctx ->
            namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
                .flatMap(namespaceModel ->
                        // List all secret-paths currently under this entity in order to collect all secrets
                        // that need to be deleted.
                        secretPathService.getPaths(namespace, entity)
                                // If a secret-paths partition had all its preexisting paths deleted then
                                // a query for all paths in that (now empty) partition might yield a "row"
                                // whose non-partition-key, non-static columns are all NULL. Such a "row"
                                // must be skipped.
                                .filter(model -> !Objects.isNull(model.getPath()))
                                .collectList()
                                .flatMap(paths -> {
                                    if (paths.isEmpty()) {
                                        // No paths to delete. Still attempt to clean up any residual entity-row in the `entities`
                                        // table further downstream.
                                        return Mono.just(namespaceModel);
                                    }
                                    return Flux.fromIterable(paths)
                                            .filter(secretPathModel -> !Boolean.TRUE.equals(secretPathModel.getIsDir()))
                                            // For each identified secret-path, attempt to delete all its secret-payloads.
                                            .flatMap(secretPathModel ->
                                                    secretVersionService.deleteSecretVersions(namespace, entity, secretPathModel.getPath())
                                                        .doOnError(error -> {
                                                                // Fail this entity-deletion API call (respond with a 500) if an
                                                                // internal error was encountered while deleting the individual
                                                                // secret-payloads themselves.
                                                                log.error("Fail to delete secret version: namespace: {}, entity: {}, path: {}",
                                                                        namespace, entity, secretPathModel.getPath(), error);
                                                                // no metrics, will surface as 500
                                                                telemetryComponents.setSpanAttribute(ctx,
                                                                        PARTIAL_DELETE_TYPE_KEY,
                                                                        PartialDeleteType.SECRET_VERSION_ON_ENTITY.name());
                                                        })
                                            )
                                            // If the deletions of all identified secrets completed without errors, proceed to
                                            // attempt deletion of all secret-paths under this entity.
                                            .then(Mono.defer(() ->
                                                    secretPathService.deleteAll(namespace, entity)
                                                        .onErrorResume(error -> {
                                                                log.error("Failed to delete secret path for entity: namespace {}, entity {}. {} orphan paths may be left behind.",
                                                                        namespace, entity, paths.size(), error);
                                                                telemetryComponents.setSpanAttribute(ctx,
                                                                        PARTIAL_DELETE_TYPE_KEY,
                                                                        PartialDeleteType.SECRET_PATH_ON_ENTITY.name());
                                                                customMetricsRegistry.recordPartialEntityDeletionOnPath(namespace);
                                                                // Even if secret-path deletion failed due to an internal error,
                                                                // we have deleted all the secrets within the entity already.
                                                                // Therefore, continue with the rest of the process and proceed
                                                                // to delete the entity-row inside the `entities` table (so that
                                                                // the HEAD entity-existence check fails for the entity once that
                                                                // step succeeds).
                                                                //
                                                                // Even though `Mono.empty()` is returned here, the
                                                                // `.then(namespaceModel)` at the end of this publisher-chain
                                                                // ensures that publishers further downstream of that will
                                                                // still execute as long as no upstream publishers returned an
                                                                // error-result.
                                                                //
                                                                // NOTE: Even if entity-deletion is allowed to return a 204 after
                                                                // this failure and HEAD existence-checks fail (as expected) after
                                                                // the deletion completes,  the secret paths which failed deletion
                                                                // would end up orphaned, and when a new secret is created with the
                                                                // same entity-type & entity-ID (which would recreate the entity-row
                                                                // inside the `entities` table), and that new secret has a path which
                                                                // collides with the preexisting paths that failed deletion, the secret
                                                                // insertion would fail.
                                                                //
                                                                // TODO: Revisit the flow given this discrepancy.
                                                                return Mono.empty();
                                                            })
                                            ));
                                })
                                .thenReturn(namespaceModel)
                )
                .flatMap(namespaceModel -> entityService.deleteEntity(namespace, entityType,
                                entityId, namespaceModel)
                        .onErrorResume(error -> {
                            log.error(
                                    "Fail to delete entity: namespace:{}, entity: {}. Orphan entity will remain",
                                    namespace,
                                    entity, error);
                            telemetryComponents.setSpanAttribute(ctx, PARTIAL_DELETE_TYPE_KEY,
                                    PartialDeleteType.ENTITY_ON_ENTITY.name());
                            customMetricsRegistry.recordPartialEntityDeletionOnEntity(
                                    namespace);
                            // Respond with a 204 even if entity-deletion encountered an internal
                            // error.
                            //
                            // This is a partial delete where all secrets have been removed but
                            // removal of other artefacts failed.
                            //
                            // NOTE: The HEAD call that checks for entity-existence will
                            // still return successfully in this circumstance even though
                            // the DELETE call returned a 204 to the client.
                            //
                            // TODO: Revisit the flow given this discrepancy.
                            return Mono.empty();
                        })
                )
                .onErrorResume(NotFoundException.class, error -> {
                    log.info("Entity or namespace does not exist: namespace:{}, entityType:{}, entityId:{}", namespace, entityType, entityId);
                    return Mono.empty(); // Return 204 if the entity or namespace does not exist
                })
                .then()
        );
    }


    public Mono<Void> entityExists(String namespace, String entityType, String entityId) {
        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
            .flatMap(namespaceModel -> entityService.entityExists(namespace, entityType, entityId, namespaceModel)
                .flatMap(exists -> {
                    if (Boolean.FALSE.equals(exists)) {
                        return Mono.error(() -> ExceptionUtils.constructErrorResponseException(
                                NotFoundException.class,
                                String.format(Constants.MSG_ENTITY_NOT_FOUND, entityType, entityId),
                                ErrorSubType.ENTITY_TYPE_NOT_FOUND));
                    }
                    return Mono.empty();
                })
            );
    }
}
