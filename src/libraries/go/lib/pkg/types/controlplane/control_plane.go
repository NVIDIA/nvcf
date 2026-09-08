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

// Package controlplane contains shared identity helpers for self-managed NVCF
// control planes.
package controlplane

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// OwnerLabel records which self-managed control plane owns an object.
	OwnerLabel = "nvcf.nvidia.com/control-plane-owner"
	// DefaultOwner is the internal owner value for the legacy, unnamed control plane.
	DefaultOwner = "default"
	// SharedOwner is the internal owner value for prerequisites shared by all planes.
	SharedOwner = "shared"

	// MaxIDLength leaves enough room for the longest legacy Cassandra keyspace.
	MaxIDLength = 32
	// CassandraIdentifierMaxLength is Cassandra's limit for unquoted keyspace and role identifiers.
	CassandraIdentifierMaxLength = 48

	nameHashLength = 8
)

var cassandraIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Identity is the validated identity of one self-managed control plane.
type Identity struct {
	id string
}

// NewIdentity validates and returns a named control-plane identity.
func NewIdentity(id string) (Identity, error) {
	if err := ValidateID(id); err != nil {
		return Identity{}, err
	}
	return Identity{id: id}, nil
}

// IdentityFromConfig returns the legacy default identity for empty config.
func IdentityFromConfig(id string) (Identity, error) {
	if id == "" {
		return DefaultIdentity(), nil
	}
	return NewIdentity(id)
}

// DefaultIdentity returns the internal identity for legacy installs.
func DefaultIdentity() Identity {
	return Identity{id: DefaultOwner}
}

// ValidateID checks user-provided named control-plane IDs.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("control plane ID is required")
	}
	if id == DefaultOwner || id == SharedOwner {
		return fmt.Errorf("control plane ID %q is reserved", id)
	}
	if len(id) > MaxIDLength {
		return fmt.Errorf("control plane ID %q exceeds %d characters", id, MaxIDLength)
	}
	if errs := validation.IsDNS1123Label(id); len(errs) > 0 {
		return fmt.Errorf("control plane ID %q must be a lowercase DNS label: %s", id, strings.Join(errs, "; "))
	}
	return nil
}

// String returns the owner-label value for the identity.
func (i Identity) String() string {
	return i.id
}

// IsDefault returns true when the identity is the legacy default plane.
func (i Identity) IsDefault() bool {
	return i.id == DefaultOwner
}

// Valid returns true when the identity was built by a supported constructor.
func (i Identity) Valid() bool {
	if i.id == DefaultOwner {
		return true
	}
	return ValidateID(i.id) == nil
}

// OwnerLabels returns the metadata labels that record ownership.
func OwnerLabels(identity Identity) (map[string]string, error) {
	if !identity.Valid() {
		return nil, invalidIdentityError(identity)
	}
	return map[string]string{OwnerLabel: identity.id}, nil
}

// AddOwnerLabel returns labels with the control-plane owner label added.
func AddOwnerLabel(labels map[string]string, identity Identity) (map[string]string, error) {
	if !identity.Valid() {
		return nil, invalidIdentityError(identity)
	}
	out := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		out[key] = value
	}
	out[OwnerLabel] = identity.id
	return out, nil
}

// IsOwnedBy returns true only when labels explicitly name this identity.
func IsOwnedBy(labels map[string]string, identity Identity) bool {
	if !identity.Valid() {
		return false
	}
	return labels[OwnerLabel] == identity.id
}

// IsSharedObject returns true when labels mark an object as shared.
func IsSharedObject(labels map[string]string) bool {
	return labels[OwnerLabel] == SharedOwner
}

// DNSLabelName derives a DNS-label Kubernetes name for one plane.
func DNSLabelName(identity Identity, legacyName string) (string, error) {
	if !identity.Valid() {
		return "", invalidIdentityError(identity)
	}
	if errs := validation.IsDNS1123Label(legacyName); len(errs) > 0 {
		return "", fmt.Errorf("legacy name %q must be a DNS label: %s", legacyName, strings.Join(errs, "; "))
	}
	if identity.IsDefault() {
		return legacyName, nil
	}

	name := identity.id + "-" + legacyName
	if len(name) > validation.DNS1123LabelMaxLength {
		name = truncateDNSLabel(name)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return "", fmt.Errorf("derived name %q must be a DNS label: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

// CassandraKeyspaceName derives a Cassandra keyspace name for one plane.
func CassandraKeyspaceName(identity Identity, legacyKeyspace string) (string, error) {
	if !identity.Valid() {
		return "", invalidIdentityError(identity)
	}
	if err := ValidateCassandraIdentifier(legacyKeyspace); err != nil {
		return "", fmt.Errorf("invalid legacy Cassandra keyspace %q: %w", legacyKeyspace, err)
	}
	if identity.IsDefault() {
		return legacyKeyspace, nil
	}

	name := strings.ReplaceAll(identity.id, "-", "_") + "_" + legacyKeyspace
	if err := ValidateCassandraIdentifier(name); err != nil {
		return "", fmt.Errorf("invalid derived Cassandra keyspace %q: %w", name, err)
	}
	return name, nil
}

// ValidateCassandraIdentifier checks Cassandra unquoted keyspace and role identifiers.
func ValidateCassandraIdentifier(identifier string) error {
	if identifier == "" {
		return fmt.Errorf("identifier is required")
	}
	if len(identifier) > CassandraIdentifierMaxLength {
		return fmt.Errorf("identifier exceeds %d characters", CassandraIdentifierMaxLength)
	}
	if !cassandraIdentifierRE.MatchString(identifier) {
		return fmt.Errorf("identifier may contain only letters, digits, and underscores")
	}
	return nil
}

func truncateDNSLabel(name string) string {
	hash := sha256.Sum256([]byte(name))
	suffix := fmt.Sprintf("%x", hash)[:nameHashLength]
	prefixLength := validation.DNS1123LabelMaxLength - len("-") - nameHashLength
	return name[:prefixLength] + "-" + suffix
}

func invalidIdentityError(identity Identity) error {
	return fmt.Errorf("invalid control plane identity %q", identity.id)
}
