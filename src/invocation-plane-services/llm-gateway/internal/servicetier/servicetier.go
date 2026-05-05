/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package servicetier

import (
	"fmt"
	"iter"
)

// Tier defines the tier of service
//
//nolint:recvcheck
type Tier string

const (
	// Auto is the auto service tier
	Auto Tier = "auto"
	// OnDemand is the on demand service tier
	OnDemand Tier = "on_demand"
	// Flex is the flex service tier
	Flex Tier = "flex"
	// Batch is the batch service tier
	Batch Tier = "batch"
	// Performance is the performance service tier
	Performance Tier = "performance"
)

func (t Tier) String() string {
	return string(t)
}

// UnmarshalText implements the encoding.TextUnmarshaler interface to enable
// reading from toml/yaml/json formats in the configuration with viper
func (t *Tier) UnmarshalText(text []byte) error {
	x := Tier(string(text))
	switch x {
	case Auto, OnDemand, Flex, Batch, Performance:
		*t = x
		return nil
	default:
		return fmt.Errorf("servicetiers: invalid tier value %q", x)
	}
}

// AllTiers returns a slice of all supported tiers. Note that this does not
// include [Auto], because it is not an actual tier.
func AllTiers() []Tier {
	return []Tier{OnDemand, Flex, Batch, Performance}
}

// AllTiersSeq returns an [iter.Seq[Tier]] that iterates through all supported
// tiers.
func AllTiersSeq() iter.Seq[Tier] {
	return func(yield func(Tier) bool) {
		switch {
		case !yield(OnDemand):
		case !yield(Flex):
		case !yield(Batch):
		case !yield(Performance):
		default:
			// success
		}
	}
}

// IsValid checks if the Tier is a valid service tier
func (t Tier) IsValid() bool {
	switch t {
	case Auto, OnDemand, Flex, Batch, Performance:
		return true
	default:
		return false
	}
}
