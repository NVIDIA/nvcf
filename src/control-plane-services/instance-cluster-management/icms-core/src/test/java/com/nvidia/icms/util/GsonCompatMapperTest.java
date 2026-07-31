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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

import com.nvidia.icms.errors.CustomErrorResponse;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatStatus;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.inbound.rest.model.instance.InstanceState;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import java.util.LinkedHashMap;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

/**
 * Characterization tests pinning the wire format previously produced by {@code new Gson()} so
 * that the Jackson-based {@link GsonCompatMapper} stays byte-compatible. Covers the two Gson
 * behaviors that ICMS relied on: null-field omission and enum/property names, plus Gson's
 * lenient (ignore-unknown) deserialization.
 */
class GsonCompatMapperTest {

    @Test
    void toJson_omitsNullFields_likeGson() {
        // Only the populated field is emitted; the null "Code" field is omitted (Gson default).
        SpotInstanceState state = SpotInstanceState.builder().name("running").build();
        assertEquals("{\"Name\":\"running\"}", GsonCompatMapper.toJson(state));

        InstanceState instanceState = InstanceState.builder().code(16).build();
        assertEquals("{\"Code\":16}", GsonCompatMapper.toJson(instanceState));
    }

    @Test
    void toJson_customErrorResponse_matchesLegacyShape() {
        CustomErrorResponse response = new CustomErrorResponse();
        response.setError("boom");
        assertEquals("{\"error\":\"boom\"}", GsonCompatMapper.toJson(response));

        // A null error field is omitted, exactly like Gson.
        assertEquals("{}", GsonCompatMapper.toJson(new CustomErrorResponse()));
    }

    @Test
    void toJson_mapValuesOmitNulls_andKeepFieldNames() {
        Map<String, Object> metadata = new LinkedHashMap<>();
        metadata.put("provider", ClusterProviderEnum.DGXCLOUD);
        metadata.put("region", ClusterRegion.US_EAST_1);
        metadata.put("missing", null);
        assertEquals("{\"provider\":\"DGX-CLOUD\",\"region\":\"us-east-1\"}",
                     GsonCompatMapper.toJson(metadata));
    }

    @ParameterizedTest
    @CsvSource({
            "GFN,GFN",
            "ONPREM,ON-PREM",
            "DGXCLOUD,DGX-CLOUD",
    })
    void cloudProvider_serializesToLegacyValue(String constant, String expected) {
        assertEquals("\"" + expected + "\"",
                     GsonCompatMapper.toJson(CloudProvider.valueOf(constant)));
    }

    @Test
    void enums_serializeToLegacySerializedNameValues() {
        assertEquals("\"DGX-CLOUD\"", GsonCompatMapper.toJson(ClusterProviderEnum.DGXCLOUD));
        assertEquals("\"us-east-1\"", GsonCompatMapper.toJson(ClusterRegion.US_EAST_1));
        assertEquals("\"eastus\"", GsonCompatMapper.toJson(ClusterRegion.EASTUS));
        assertEquals("\"RESERVED_BACKUP\"", GsonCompatMapper.toJson(CapacityType.RESERVED_BACKUP));
        assertEquals("\"CORDON_AND_DRAIN\"",
                     GsonCompatMapper.toJson(ClusterStatusEnum.CORDON_AND_DRAIN));
        assertEquals("\"healthy\"", GsonCompatMapper.toJson(ClusterHeartbeatStatus.HEALTHY));
        assertEquals("\"unhealthy\"", GsonCompatMapper.toJson(CloudHealthStatus.UNHEALTHY));
        assertEquals("\"ngc-managed\"", GsonCompatMapper.toJson(ClusterSource.NGC_MANAGED));
        assertEquals("\"SINGLE\"", GsonCompatMapper.toJson(NodeTypeEnum.SINGLE));
        assertEquals("\"CONTAINER\"", GsonCompatMapper.toJson(InstanceTypeUsageEnum.CONTAINER));
        assertEquals("\"UPLOAD\"", GsonCompatMapper.toJson(ResultHandlingStrategy.UPLOAD));
        assertEquals("\"instance-terminated-no-capacity\"",
                     GsonCompatMapper.toJson(SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY));
        assertEquals("\"shutting-down\"",
                     GsonCompatMapper.toJson(SpotInstanceInternalState.SHUTTING_DOWN));
        assertEquals("\"CancelSpotInstanceRequests\"",
                     GsonCompatMapper.toJson(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS));
    }

    @Test
    void fromJson_returnsNull_forNullInput_likeGson() {
        assertNull(GsonCompatMapper.fromJson(null, CustomErrorResponse.class));
    }

    @Test
    void fromJson_ignoresUnknownProperties_likeGson() {
        CustomErrorResponse response =
                GsonCompatMapper.fromJson("{\"error\":\"x\",\"unexpected\":123}",
                                          CustomErrorResponse.class);
        assertEquals("x", response.getError());
    }

    @Test
    void roundTrip_preservesPopulatedFields() {
        SpotInstanceState original = SpotInstanceState.builder().code(16).name("running").build();
        SpotInstanceState restored =
                GsonCompatMapper.fromJson(GsonCompatMapper.toJson(original), SpotInstanceState.class);
        assertEquals(original, restored);
    }
}
