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

package physical

import (
	"encoding/hex"
	"fmt"
)

// Entry is used to represent data stored by the physical backend
type Entry struct {
	Key      string
	Value    []byte
	SealWrap bool `json:"seal_wrap,omitempty"`

	// Only used in replication
	ValueHash []byte
}

func (e *Entry) String() string {
	return fmt.Sprintf("Key: %s. SealWrap: %t. Value: %s. ValueHash: %s", e.Key, e.SealWrap, hex.EncodeToString(e.Value), hex.EncodeToString(e.ValueHash))
}
