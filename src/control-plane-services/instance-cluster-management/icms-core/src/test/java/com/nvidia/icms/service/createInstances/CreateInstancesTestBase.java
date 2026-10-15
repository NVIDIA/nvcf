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
package com.nvidia.icms.service.createInstances;

import static com.nvidia.icms.util.TestUtil.DUMMY_ATTRIBUTES;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_REGION;

import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;

public class CreateInstancesTestBase {
    public static final String DUMMY_INSTANCE_TYPE = "dummy-instance-type";
    public static final String DUMMY_INSTANCE_TYPE_VALUE = "dummy-instance-type-value";
    public static final int DUMMY_GPU_COUNT = 1;
    public static final String TEST_REGION = "region";


    // Helper methods for creating test objects
    public SpotInstanceRequestSchema createInstanceRequest() {
        SpotInstanceRequestSchema request = new SpotInstanceRequestSchema();
        request.setNcaId(DUMMY_BYOC_NCA_ID);
        request.setGpu(DUMMY_GPU);
        request.setInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE);
        request.setRegions(Set.of(DUMMY_REGION));
        request.setClusters(Set.of(DUMMY_BYOC_CLUSTER_NAME));
        request.setAttributes(DUMMY_ATTRIBUTES);
        return request;
    }

    public ClusterByGroupIdAndIdEntity createClusterEntity() {
        ClusterByGroupIdAndIdEntity entity = new ClusterByGroupIdAndIdEntity();
        ClusterByGroupIdAndIdKey key = ClusterByGroupIdAndIdKey.builder()
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .clusterId(DUMMY_CLUSTER_ID)
                .build();
        entity.setKey(key);
        entity.setClusterName(DUMMY_BYOC_CLUSTER_NAME);
        entity.setRegion(DUMMY_REGION);
        entity.setAttributes(DUMMY_ATTRIBUTES);
        entity.setAllowClusterTargeting(true);
        entity.setClusterProvider(ClusterProviderEnum.AWS);
        entity.setAllowClusterTargeting(true);
        entity.setGpusV5(Set.of(createGpuV5()));
        return entity;
    }

    public CloudHealthEntity createCloudHealthEntity() {
        CloudHealthEntity entity = new CloudHealthEntity();
        CloudHealthKey key = new CloudHealthKey(ResourceProvider.BYOC, DUMMY_CLUSTER_ID);
        entity.setKey(key);

        entity.setStatus(CloudHealthStatus.HEALTHY);

        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(DUMMY_GPU, createGpuCapacity(1));
        entity.setGpuUsage(gpuUsage);

        return entity;
    }

    public GpuCapacity createGpuCapacity(int available) {
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(available);
        return capacity;
    }

    public GpuV5Udt createGpuV5() {
        GpuV5Udt gpu = new GpuV5Udt();
        gpu.setName(DUMMY_GPU);
        gpu.setInstanceTypes(Set.of(createInstanceTypeV5()));
        return gpu;
    }

    public InstanceTypeV5Udt createInstanceTypeV5() {
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setName(DUMMY_NON_BYOC_INSTANCE_TYPE);
        instanceType.setGpuCount(1);
        return instanceType;
    }


    // Helper methods for creating test objects
    public ClustersByAuthorizedAccountsEntity createNvcaCluster(String clusterGroupId, String clusterGroupName, String clusterName) {
        ClustersByAuthorizedAccountsEntity entity = new ClustersByAuthorizedAccountsEntity();
        ClustersByAuthorizedAccountsKey key = ClustersByAuthorizedAccountsKey.builder()
                .ncaIdKey(DUMMY_BYOC_NCA_ID)
                .clusterId(clusterName)
                .build();
        entity.setKey(key);
        entity.setClusterGroupId(clusterGroupId);
        entity.setClusterGroupName(clusterGroupName);
        entity.setClusterName(clusterName);
        entity.setNcaId(DUMMY_BYOC_NCA_ID);
        entity.setGpusV5(Set.of(createGpuV5()));
        return entity;
    }

    public ClustersByAuthorizedAccountsEntity createNvcaClusterWithCustomGpu(String clusterGroupId, String clusterGroupName, String gpuName) {
        ClustersByAuthorizedAccountsEntity cluster = new ClustersByAuthorizedAccountsEntity();
        ClustersByAuthorizedAccountsKey key = ClustersByAuthorizedAccountsKey.builder()
                .ncaIdKey(DUMMY_BYOC_NCA_ID)
                .clusterId(clusterGroupName)
                .build();
        cluster.setKey(key);
        cluster.setClusterGroupId(clusterGroupId);
        cluster.setClusterGroupName(clusterGroupName);
        cluster.setNcaId(DUMMY_BYOC_NCA_ID);

        GpuV5Udt gpu = new GpuV5Udt();
        gpu.setName(gpuName);
        gpu.setInstanceTypes(Set.of(createInstanceTypeV5()));
        cluster.setGpusV5(Set.of(gpu));
        return cluster;
    }

    public ClustersByAuthorizedAccountsEntity createNvcaClusterWithCustomInstanceType(String clusterGroupId, String clusterGroupName, String instanceTypeName) {
        ClustersByAuthorizedAccountsEntity cluster = new ClustersByAuthorizedAccountsEntity();
        ClustersByAuthorizedAccountsKey key = ClustersByAuthorizedAccountsKey.builder()
                .ncaIdKey(DUMMY_BYOC_NCA_ID)
                .clusterId(clusterGroupName)
                .build();
        cluster.setKey(key);
        cluster.setClusterGroupId(clusterGroupId);
        cluster.setClusterGroupName(clusterGroupName);
        cluster.setNcaId(DUMMY_BYOC_NCA_ID);

        GpuV5Udt gpu = new GpuV5Udt();
        gpu.setName(DUMMY_GPU);
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setName(instanceTypeName);
        instanceType.setGpuCount(1);
        gpu.setInstanceTypes(Set.of(instanceType));
        cluster.setGpusV5(Set.of(gpu));
        return cluster;
    }

    public InstanceTypeUdt createInstanceTypeUdt() {
        InstanceTypeUdt instanceType = new InstanceTypeUdt();
        instanceType.setName(DUMMY_INSTANCE_TYPE);
        instanceType.setValue(DUMMY_INSTANCE_TYPE_VALUE);
        instanceType.setGpuCount(1);
        return instanceType;
    }

    public ClustersByAuthorizedAccountsEntity createClusterForByoc(String ncaId, String clusterGroupName, String clusterGroupId, String clusterId) {
        return createClusterForByoc(ncaId, clusterGroupName, clusterGroupId, clusterId, DUMMY_GPU, DUMMY_NON_BYOC_INSTANCE_TYPE);
    }

    public ClustersByAuthorizedAccountsEntity createClusterForByoc(String ncaId, String clusterGroupName, String clusterGroupId, String clusterId, String gpuName, String instanceName) {
        ClustersByAuthorizedAccountsKey key = ClustersByAuthorizedAccountsKey.builder()
                .ncaIdKey(ncaId)
                .clusterId(clusterId)
                .build();

        Set<GpuV5Udt> gpusV5 = new HashSet<>();
        gpusV5.add(GpuV5Udt.builder()
                           .name(gpuName)
                           .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                                                         .name(instanceName)
                                                         .value(instanceName)
                                                         .build()))
                           .build());

        return  ClustersByAuthorizedAccountsEntity.builder()
                .key(key)
                .clusterGroupName(clusterGroupName)
                .clusterGroupId(clusterGroupId)
                .creationQueues(Map.of(gpuName,
                                       CreationQueueUdt.builder()
                                               .url(RandomFactory.getRandomStringWithPrefix("queueUrl", 5))
                                               .queueType("fifo")
                                               .build()))
                .gpusV5(gpusV5)
                .authorizedNcaIds(Set.of("*"))
                .ncaId(ncaId)
                .build();
    }


    public InstanceTypeUdt createInstanceTypeWithCustomValue(String value) {
        InstanceTypeUdt instanceType = new InstanceTypeUdt();
        instanceType.setName(DUMMY_INSTANCE_TYPE);
        instanceType.setValue(value);
        instanceType.setGpuCount(DUMMY_GPU_COUNT);
        return instanceType;
    }


    public InstanceTypeUdt createInstanceTypeWithCustomGpuCount(int gpuCount) {
        InstanceTypeUdt instanceType = new InstanceTypeUdt();
        instanceType.setName(DUMMY_INSTANCE_TYPE);
        instanceType.setValue(DUMMY_INSTANCE_TYPE_VALUE);
        instanceType.setGpuCount(gpuCount);
        return instanceType;
    }

    public RequestInstanceDestination createDestination(CloudProvider cloudProvider) {
        return RequestInstanceDestination.builder()
                .cloudProvider(cloudProvider)
                .region(TEST_REGION)
                .clusterName(DUMMY_BYOC_CLUSTER_NAME  )
                .clusterGroupName(DUMMY_BYOC_CLUSTER_GROUP_NAME  )
                .build();
    }

    public static ClusterEntity toClusterEntity(
            ClustersByAuthorizedAccountsEntity entity) {
        return ClusterEntity.builder()
                .clusterId(entity.getKey().getClusterId())
                .ncaId(entity.getNcaId())
                .clusterName(entity.getClusterName())
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueues(entity.getCreationQueues())
                .gpusV4(entity.getGpusV4())
                .gpusV5(entity.getGpusV5())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }
}
