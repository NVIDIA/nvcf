// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package viper

// ExperimentalBindStruct tells Viper to use the new bind struct feature.
func ExperimentalBindStruct() Option {
	return optionFunc(func(v *Viper) {
		v.experimentalBindStruct = true
	})
}
