/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controlplane

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name: "short ID",
			id:   "plane-a",
		},
		{
			name: "maximum ID length",
			id:   strings.Repeat("a", MaxIDLength),
		},
		{
			name:    "empty ID is not a named identity",
			id:      "",
			wantErr: true,
		},
		{
			name:    "default is reserved",
			id:      DefaultOwner,
			wantErr: true,
		},
		{
			name:    "shared is reserved",
			id:      SharedOwner,
			wantErr: true,
		},
		{
			name:    "underscore is rejected",
			id:      "plane_a",
			wantErr: true,
		},
		{
			name:    "uppercase is rejected",
			id:      "Plane-A",
			wantErr: true,
		},
		{
			name:    "leading hyphen is rejected",
			id:      "-plane-a",
			wantErr: true,
		},
		{
			name:    "trailing hyphen is rejected",
			id:      "plane-a-",
			wantErr: true,
		},
		{
			name:    "longer than Cassandra budget is rejected",
			id:      strings.Repeat("a", MaxIDLength+1),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateID(tt.id)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIdentityConstructors(t *testing.T) {
	named, err := NewIdentity("plane-a")
	require.NoError(t, err)
	assert.True(t, named.Valid())
	assert.False(t, named.IsDefault())
	assert.Equal(t, "plane-a", named.String())

	legacy, err := IdentityFromConfig("")
	require.NoError(t, err)
	assert.True(t, legacy.Valid())
	assert.True(t, legacy.IsDefault())
	assert.Equal(t, DefaultOwner, legacy.String())

	_, err = NewIdentity("")
	require.Error(t, err)

	_, err = NewIdentity(DefaultOwner)
	require.Error(t, err)

	var zero Identity
	assert.False(t, zero.Valid())
	assert.False(t, zero.IsDefault())
	assert.Empty(t, zero.String())
}

func TestOwnerLabels(t *testing.T) {
	identity, err := NewIdentity("plane-a")
	require.NoError(t, err)

	labels, err := OwnerLabels(identity)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{OwnerLabel: "plane-a"}, labels)

	existing := map[string]string{"app": "api"}
	withOwner, err := AddOwnerLabel(existing, identity)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"app":      "api",
		OwnerLabel: "plane-a",
	}, withOwner)

	existing["app"] = "mutated"
	assert.Equal(t, "api", withOwner["app"])

	var zero Identity
	_, err = OwnerLabels(zero)
	require.Error(t, err)

	_, err = AddOwnerLabel(nil, zero)
	require.Error(t, err)
}

func TestIsOwnedBy(t *testing.T) {
	planeA, err := NewIdentity("plane-a")
	require.NoError(t, err)
	planeB, err := NewIdentity("plane-b")
	require.NoError(t, err)
	legacy := DefaultIdentity()

	tests := []struct {
		name     string
		labels   map[string]string
		identity Identity
		want     bool
	}{
		{
			name:     "named plane owns matching label",
			labels:   map[string]string{OwnerLabel: "plane-a"},
			identity: planeA,
			want:     true,
		},
		{
			name:     "named plane does not own another plane label",
			labels:   map[string]string{OwnerLabel: "plane-b"},
			identity: planeA,
		},
		{
			name:     "named plane does not own missing label",
			labels:   map[string]string{},
			identity: planeA,
		},
		{
			name:     "named plane does not own shared label",
			labels:   map[string]string{OwnerLabel: SharedOwner},
			identity: planeA,
		},
		{
			name:     "legacy plane owns explicit default label",
			labels:   map[string]string{OwnerLabel: DefaultOwner},
			identity: legacy,
			want:     true,
		},
		{
			name:     "legacy plane does not own missing label",
			labels:   map[string]string{},
			identity: legacy,
		},
		{
			name:     "zero identity does not own matching-looking empty label",
			labels:   map[string]string{OwnerLabel: ""},
			identity: Identity{},
		},
		{
			name:     "identity comparisons do not use name prefixes",
			labels:   map[string]string{OwnerLabel: "plane-b-extra"},
			identity: planeB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsOwnedBy(tt.labels, tt.identity))
		})
	}

	assert.True(t, IsSharedObject(map[string]string{OwnerLabel: SharedOwner}))
	assert.False(t, IsSharedObject(map[string]string{OwnerLabel: "plane-a"}))
}

func TestDNSLabelName(t *testing.T) {
	legacy := DefaultIdentity()
	planeA, err := NewIdentity("plane-a")
	require.NoError(t, err)

	name, err := DNSLabelName(legacy, "api")
	require.NoError(t, err)
	assert.Equal(t, "api", name)

	name, err = DNSLabelName(planeA, "api")
	require.NoError(t, err)
	assert.Equal(t, "plane-a-api", name)

	maxID, err := NewIdentity(strings.Repeat("a", MaxIDLength))
	require.NoError(t, err)
	longLegacyName := "this-is-a-very-long-legacy-resource-name-that-must-be-shortened"
	name, err = DNSLabelName(maxID, longLegacyName)
	require.NoError(t, err)
	assert.Len(t, name, validation.DNS1123LabelMaxLength)
	assert.Empty(t, validation.IsDNS1123Label(name))
	assert.True(t, strings.HasPrefix(name, strings.Repeat("a", MaxIDLength)+"-"))

	nameAgain, err := DNSLabelName(maxID, longLegacyName)
	require.NoError(t, err)
	assert.Equal(t, name, nameAgain)

	planeB, err := NewIdentity("plane-b")
	require.NoError(t, err)
	planeBName, err := DNSLabelName(planeB, "api")
	require.NoError(t, err)
	assert.NotEqual(t, name, planeBName)

	_, err = DNSLabelName(planeA, "not_a_dns_label")
	require.Error(t, err)

	_, err = DNSLabelName(Identity{}, "api")
	require.Error(t, err)
}

func TestCassandraKeyspaceName(t *testing.T) {
	legacy := DefaultIdentity()
	planeA, err := NewIdentity("plane-a")
	require.NoError(t, err)

	keyspace, err := CassandraKeyspaceName(legacy, "nvcf_api")
	require.NoError(t, err)
	assert.Equal(t, "nvcf_api", keyspace)

	keyspace, err = CassandraKeyspaceName(planeA, "nvcf_api")
	require.NoError(t, err)
	assert.Equal(t, "plane_a_nvcf_api", keyspace)

	maxID, err := NewIdentity(strings.Repeat("a", MaxIDLength))
	require.NoError(t, err)
	keyspace, err = CassandraKeyspaceName(maxID, "nvcf_autoscaler")
	require.NoError(t, err)
	assert.Len(t, keyspace, CassandraIdentifierMaxLength)
	assert.Empty(t, ValidateCassandraIdentifier(keyspace))

	planeWithHyphen, err := NewIdentity("a-b")
	require.NoError(t, err)
	planeWithoutHyphen, err := NewIdentity("ab")
	require.NoError(t, err)

	hyphenKeyspace, err := CassandraKeyspaceName(planeWithHyphen, "nvcf_api")
	require.NoError(t, err)
	plainKeyspace, err := CassandraKeyspaceName(planeWithoutHyphen, "nvcf_api")
	require.NoError(t, err)
	assert.NotEqual(t, hyphenKeyspace, plainKeyspace)

	_, err = CassandraKeyspaceName(planeA, "not-a-keyspace")
	require.Error(t, err)

	_, err = CassandraKeyspaceName(planeA, strings.Repeat("a", CassandraIdentifierMaxLength))
	require.Error(t, err)

	_, err = CassandraKeyspaceName(Identity{}, "nvcf_api")
	require.Error(t, err)
}

func TestValidateCassandraIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		wantErr    bool
	}{
		{
			name:       "legacy keyspace",
			identifier: "nvcf_autoscaler",
		},
		{
			name:       "maximum length identifier",
			identifier: strings.Repeat("a", CassandraIdentifierMaxLength),
		},
		{
			name:    "empty identifier",
			wantErr: true,
		},
		{
			name:       "hyphen is rejected",
			identifier: "nvcf-api",
			wantErr:    true,
		},
		{
			name:       "dot is rejected",
			identifier: "nvcf.api",
			wantErr:    true,
		},
		{
			name:       "too long",
			identifier: strings.Repeat("a", CassandraIdentifierMaxLength+1),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCassandraIdentifier(tt.identifier)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
