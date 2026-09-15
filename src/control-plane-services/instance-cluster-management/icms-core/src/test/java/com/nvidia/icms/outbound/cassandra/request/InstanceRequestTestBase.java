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
package com.nvidia.icms.outbound.cassandra.request;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Optional;
import java.util.UUID;

public class InstanceRequestTestBase extends IntegrationTest {

    /********  Helper functions *******/

    void assertInstanceRequestV2Entity(Optional<InstanceRequestV2Entity> instanceRequestV2Entity, String requestId, String customer, Instant createTime ) {
        assertTrue(instanceRequestV2Entity.isPresent());
        if (customer != null) {
            assertEquals(customer, instanceRequestV2Entity.get().getCustomer());
        }
        if (requestId != null) {
            assertEquals(requestId, instanceRequestV2Entity.get().getRequestId());
        }

        if (createTime != null) {
            assertEquals(createTime.truncatedTo(ChronoUnit.MILLIS),
                         TimeUtils.getInstantFromUuid(instanceRequestV2Entity.get().getCreateTimeuuid()));
        }
    }

    /**
     * Compares all fields between two InstanceRequestV2Entity objects
     * @param expected The expected InstanceRequestV2Entity
     * @param actual The actual InstanceRequestV2Entity to compare against
     */
    void assertInstanceRequestV2EntityEquals(InstanceRequestV2Entity expected, InstanceRequestV2Entity actual) {
        assertEquals(expected.getRequestId(), actual.getRequestId(), "Request ID mismatch");
        assertEquals(expected.getCreateTimeuuid(), actual.getCreateTimeuuid(), "Create time UUID mismatch");
        assertEquals(expected.getCustomer(), actual.getCustomer(), "Customer mismatch");
        assertEquals(expected.getAction(), actual.getAction(), "Action mismatch");
        assertEquals(expected.getRequest(), actual.getRequest(), "Request mismatch");
        assertEquals(expected.getState(), actual.getState(), "State mismatch");
        assertEquals(expected.getStatusCode(), actual.getStatusCode(), "Status code mismatch");
        assertEquals(expected.getStatusMessage(), actual.getStatusMessage(), "Status message mismatch");
        assertEquals(expected.getStatusUpdateTime(), actual.getStatusUpdateTime(), "Status update time mismatch");
        assertEquals(expected.getResourceProvider(), actual.getResourceProvider(), "Resource provider mismatch");
        assertEquals(expected.getCheckBatchwiseInfo(), actual.getCheckBatchwiseInfo(), "Check batchwise info mismatch");
        assertEquals(expected.getClusters(), actual.getClusters(), "Clusters mismatch");
        assertEquals(expected.getRegions(), actual.getRegions(), "Regions mismatch");
        assertEquals(expected.getAttributes(), actual.getAttributes(), "Attributes mismatch");
        assertEquals(expected.getCustomAttributes(), actual.getCustomAttributes(), "Custom attributes mismatch");
        assertEquals(expected.getInstanceCount(), actual.getInstanceCount(), "Instance count mismatch");
        assertEquals(expected.getTaskId(), actual.getTaskId(), "Task ID mismatch");
        assertEquals(expected.getMaxQueuedDuration(), actual.getMaxQueuedDuration(), "Max queued duration mismatch");
        assertEquals(expected.getAccountName(), actual.getAccountName(), "Account name mismatch");
        assertEquals(expected.getFunctionId(), actual.getFunctionId(), "Function ID mismatch");
        assertEquals(expected.getFunctionVersionId(), actual.getFunctionVersionId(), "Function version ID mismatch");
        assertEquals(expected.getDeploymentId(), actual.getDeploymentId(), "Deployment ID mismatch");
        assertEquals(expected.getGpuSpecificationId(), actual.getGpuSpecificationId(), "GPU specification ID mismatch");
        assertEquals(expected.getNcaId(), actual.getNcaId(), "NCA ID mismatch");
    }

    void assertInstanceRequestV2ByDayEntity(Optional<InstanceRequestV2ByDayEntity> instanceRequestV2ByDayEntity,
                                        String requestId,
                                        Instant createTime ) {
        assertTrue(instanceRequestV2ByDayEntity.isPresent());


        if (createTime != null) {
            assertEquals(createTime.truncatedTo(ChronoUnit.DAYS),
                         instanceRequestV2ByDayEntity.get().getKey().getTruncatedTsByDay());
            assertEquals(createTime.truncatedTo(ChronoUnit.MILLIS), TimeUtils.getInstantFromUuid(instanceRequestV2ByDayEntity.get().getCreateTimeuuid()));
        }
        if (requestId != null) {
            assertEquals(requestId, instanceRequestV2ByDayEntity.get().getKey().getRequestId());
        }
    }

}
