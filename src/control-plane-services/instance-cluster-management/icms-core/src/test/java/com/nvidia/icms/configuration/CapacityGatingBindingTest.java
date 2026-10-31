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
package com.nvidia.icms.configuration;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.CapacityGatingProperties;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.source.ConfigurationPropertySources;
import org.springframework.boot.context.properties.source.MapConfigurationPropertySource;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.MutablePropertySources;
import org.springframework.core.env.PropertySource;
import org.springframework.core.io.ByteArrayResource;

/**
 * Pins how {@code icms.capacity-gating} merges across property sources, which is what the rollout
 * sequence depends on.
 *
 * <p>SIS reads the packaged {@code application-{profile}.yaml} first and then overlays the
 * {@code gfn-spot} and {@code gfn-spot-{profile}} ConfigMaps on top. For a map, the binder unions
 * keys across sources and lets the highest-precedence source win per key. That is why shipping a
 * policy in the jar is safe during the rollout, and equally why removing one later is an image
 * change: a ConfigMap can add or replace an entry but has no way to delete one the jar declares.
 */
class CapacityGatingBindingTest {

    private static final String PREFIX = "icms.capacity-gating.";

    private static final String JAR_NCA_ID = "jar-nca";
    private static final String CONFIGMAP_NCA_ID = "configmap-nca";

    /** Binds with {@code configMap} taking precedence over {@code jar}, as the runtime does. */
    private static CapacityGatingProperties bind(Map<String, Object> jar,
                                                    Map<String, Object> configMap) {
        Binder binder = new Binder(List.of(new MapConfigurationPropertySource(configMap),
                                           new MapConfigurationPropertySource(jar)));
        return binder.bind("icms", Bindable.of(CapacityGatingProperties.class))
                .orElseGet(CapacityGatingProperties::new);
    }

    private static Map<String, Object> gpuPolicy(String ncaId, String gpu,
                                                 String... instanceTypes) {
        Map<String, Object> properties = new LinkedHashMap<>();
        for (int i = 0; i < instanceTypes.length; i++) {
            properties.put(PREFIX + ncaId + ".gpus." + gpu + "[" + i + "]", instanceTypes[i]);
        }
        return properties;
    }

    /**
     * Binds from real YAML rather than from pre-flattened property names. How YAML renders an
     * empty map, an empty list and an absent value is exactly what decides the cases below, so
     * hand-writing the flattened form would assume away the thing under test.
     */
    private static CapacityGatingProperties bindYaml(String yaml) {
        List<PropertySource<?>> loaded;
        try {
            loaded = new YamlPropertySourceLoader().load(
                    "capacity-gating",
                    new ByteArrayResource(yaml.getBytes(StandardCharsets.UTF_8)));
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        MutablePropertySources sources = new MutablePropertySources();
        loaded.forEach(sources::addLast);
        return new Binder(ConfigurationPropertySources.from(sources))
                .bind("icms", Bindable.of(CapacityGatingProperties.class))
                .orElseGet(CapacityGatingProperties::new);
    }

    /**
     * Both entries survive: the ConfigMap adds its own account without displacing the one the jar
     * declares. This is the guarantee that lets a policy ship in the image while the ConfigMap
     * catches up.
     */
    @Test
    void entriesFromBothSourcesSurvive() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                gpuPolicy(CONFIGMAP_NCA_ID, "B40", "gb40_1.br30_2xlarge"));

        assertEquals(Set.of(JAR_NCA_ID, CONFIGMAP_NCA_ID), props.getCapacityGating().keySet());
        assertEquals(Map.of("L40S", List.of("gl40s_1.br25_2xlarge")),
                     props.getCapacityGating().get(JAR_NCA_ID).getGpus());
        assertEquals(Map.of("B40", List.of("gb40_1.br30_2xlarge")),
                     props.getCapacityGating().get(CONFIGMAP_NCA_ID).getGpus());

        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"));
        assertTrue(props.isInstanceTypeAllowedForNca(CONFIGMAP_NCA_ID, "B40",
                                                     "gb40_1.br30_2xlarge"));
    }

    /**
     * Within one account the higher-precedence list replaces rather than appends, so a ConfigMap
     * can narrow or widen a policy the jar shipped.
     */
    @Test
    void configMapReplacesTheJarListForTheSameAccount() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge", "gb40_2.br30_large"),
                gpuPolicy(JAR_NCA_ID, "B40", "gb40_4.br30_small"));

        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_4.br30_small"));
        assertFalse(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
        assertFalse(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_2.br30_large"));
    }

    /**
     * The one-way door. A ConfigMap that declares other accounts, or declares the key empty,
     * cannot drop the jar's entry, so ungating an account shipped in the image needs a new image.
     */
    @Test
    void configMapCannotDeleteAnEntryTheJarDeclares() {
        CapacityGatingProperties withOtherAccount = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                gpuPolicy(CONFIGMAP_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
        assertTrue(withOtherAccount.hasCapacityGating(JAR_NCA_ID));

        CapacityGatingProperties withEmptyOverride = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                Map.of(PREFIX.substring(0, PREFIX.length() - 1), Map.of()));
        assertTrue(withEmptyOverride.hasCapacityGating(JAR_NCA_ID));
        assertTrue(withEmptyOverride.isInstanceTypeAllowedForNca(JAR_NCA_ID, "L40S",
                                                                 "gl40s_1.br25_2xlarge"));
    }

    /**
     * The compiled lookup is rebuilt by the setter the binder calls, so a bound policy is in force
     * without waiting for the {@code @PostConstruct} hook.
     */
    @Test
    void boundPolicyIsInForceImmediately() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"), Map.of());

        assertTrue(props.hasCapacityGating(JAR_NCA_ID));
        assertFalse(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
        assertFalse(props.hasCapacityGating(CONFIGMAP_NCA_ID));
    }

    /**
     * Migrating a ConfigMap means moving the GPU map down one level under {@code gpus}. Writing
     * the old shape straight under the new key makes every property below the NCA ID unknown, and
     * because unknown properties are ignored rather than rejected the binder produces no map entry
     * at all. The account is left ungated, not denied.
     *
     * <p>That direction is deliberate to record: a half-converted entry over-grants rather than
     * locking an account out, so it cannot cause a lost-access complaint, but it also will not
     * announce itself. The only signal is the absence of the account from the policy map.
     */
    @Test
    void oldGpuGatingShapeUnderTheNewKeyLeavesTheAccountUngated() {
        Map<String, Object> source = new LinkedHashMap<>();
        source.put(PREFIX + JAR_NCA_ID + ".L40S[0]", "gl40s_1.br25_2xlarge");
        // A correctly shaped neighbour, so a failure here cannot be mistaken for the whole bean
        // failing to bind.
        source.putAll(gpuPolicy(CONFIGMAP_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"));

        CapacityGatingProperties props = bind(source, Map.of());

        assertTrue(props.hasCapacityGating(CONFIGMAP_NCA_ID));

        assertFalse(props.getCapacityGating().containsKey(JAR_NCA_ID));
        assertFalse(props.hasCapacityGating(JAR_NCA_ID));
        assertFalse(props.isOrgDenied(JAR_NCA_ID));
        // Ungated means unrestricted, including GPUs the half-converted entry meant to exclude.
        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
    }

    /**
     * {@code icms.gpu-allowed-nca-ids} moved onto this bean, and it is the key that restricts
     * limited GPUs in production. Bound here from a ConfigMap-shaped source so the move is proven
     * against the binder and not just against a setter.
     */
    @Test
    void gpuAllowedNcaIdsStillBindsOntoThisBean() {
        CapacityGatingProperties props = bind(Map.of(),
                Map.of("icms.gpu-allowed-nca-ids.B40[0]", "granted-nca",
                       "icms.gpu-allowed-nca-ids.B40[1]", "second-granted-nca"));

        assertEquals(Map.of("B40", List.of("granted-nca", "second-granted-nca")),
                     props.getGpuAllowedNcaIds());

        assertTrue(props.isNcaAllowedForGpu("B40", "granted-nca"));
        assertTrue(props.isNcaAllowedForGpu("B40", "second-granted-nca"));
        assertFalse(props.isNcaAllowedForGpu("B40", "other-nca"));
        // A GPU the key does not name stays unrestricted.
        assertTrue(props.isNcaAllowedForGpu("L40S", "other-nca"));
    }

    /**
     * An NCA ID written with nothing under it. YAML renders the absent value as an empty string,
     * which still counts as a property at that name, so the entry exists with both fields unset
     * and the fail-closed branch denies the account.
     */
    @Test
    void ncaIdWithNothingUnderItIsDenied() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                """);

        assertTrue(props.isOrgDenied(JAR_NCA_ID));
        assertTrue(props.hasCapacityGating(JAR_NCA_ID));
        assertFalse(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-a"));
        assertFalse(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
    }

    /**
     * An empty {@code gpus} map binds to an empty map, not to null, so the entry survives and the
     * account is not denied. An empty GPU map places no GPU restriction, so the account receives
     * every GPU and every cluster.
     *
     * <p>Worth pinning because it reads like a restriction and behaves like its opposite, and
     * because it points the other way from {@code clusters: []}, which denies everything.
     */
    @Test
    void emptyGpusMapRestrictsNothing() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                      gpus: {}
                """);

        assertEquals(Map.of(), props.getCapacityGating().get(JAR_NCA_ID).getGpus());
        assertFalse(props.isOrgDenied(JAR_NCA_ID));
        assertTrue(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
        assertTrue(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-a"));
    }

    /**
     * A {@code gpus} key written with nothing under it behaves the same as {@code gpus: {}}: it
     * places no GPU restriction. Pinned alongside a cluster list because that is the realistic
     * way to reach it, by converting an entry and leaving the GPU map to be filled in later.
     *
     * <p>The cluster list still applies, so the entry narrows clusters while quietly opening
     * every GPU, which is the opposite of what a half-written entry looks like it does.
     */
    @Test
    void gpusKeyWithNothingUnderItRestrictsNoGpu() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                      clusters:
                        - cluster-a
                      gpus:
                """);

        assertEquals(Map.of(), props.getCapacityGating().get(JAR_NCA_ID).getGpus());
        assertFalse(props.isOrgDenied(JAR_NCA_ID));

        assertTrue(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));

        // The cluster list is unaffected and still restricts.
        assertTrue(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-a"));
        assertFalse(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-b"));
    }

    /**
     * An empty {@code clusters} list is the deliberate kill switch. YAML renders it as an empty
     * string, which binds to an empty list, so unlike the empty gpus map it does reach the
     * compile step and denies the account everything.
     */
    @Test
    void emptyClustersListDeniesTheAccount() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                      clusters: []
                """);

        assertEquals(List.of(), props.getCapacityGating().get(JAR_NCA_ID).getClusters());
        assertTrue(props.isOrgDenied(JAR_NCA_ID));
        assertFalse(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-a"));
        assertFalse(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
    }

    /**
     * A GPU mapped to an empty instance-type list denies that GPU rather than permitting all of
     * them, so a truncated entry cannot widen access. The account itself stays gated, not denied.
     */
    @Test
    void emptyInstanceTypeListDeniesThatGpuOnly() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                      gpus:
                        B40: []
                        L40S:
                          - gl40s_1.br25_2xlarge
                """);

        assertFalse(props.isOrgDenied(JAR_NCA_ID));
        assertFalse(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
        assertTrue(props.isGpuAllowedForNca(JAR_NCA_ID, "L40S"));
    }

    /**
     * The wildcard wins over the cluster IDs listed beside it. An operator who adds {@code "*"}
     * to an existing list opens every cluster rather than the ones still named, so the extra
     * entries become dead weight instead of a narrowing.
     */
    @Test
    void wildcardMixedWithClusterIdsAllowsEveryCluster() {
        CapacityGatingProperties props = bindYaml("""
                icms:
                  capacity-gating:
                    jar-nca:
                      clusters:
                        - "*"
                        - cluster-a
                """);

        assertEquals(List.of("*", "cluster-a"),
                     props.getCapacityGating().get(JAR_NCA_ID).getClusters());
        assertFalse(props.isOrgDenied(JAR_NCA_ID));
        assertTrue(props.isClusterAllowedForNca(JAR_NCA_ID, "cluster-a"));
        assertTrue(props.isClusterAllowedForNca(JAR_NCA_ID, "some-other-cluster"));
    }
}
