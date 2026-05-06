// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package unstable

// The Unmarshaler interface may be implemented by types to customize their
// behavior when being unmarshaled from a TOML document.
type Unmarshaler interface {
	UnmarshalTOML(value *Node) error
}
