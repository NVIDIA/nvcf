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

package consts

import "fmt"

var PluginTypes = []PluginType{
	PluginTypeUnknown,
	PluginTypeCredential,
	PluginTypeDatabase,
	PluginTypeSecrets,
}

type PluginType uint32

// This is a list of PluginTypes used by Vault.
// If we need to add any in the future, it would
// be best to add them to the _end_ of the list below
// because they resolve to incrementing numbers,
// which may be saved in state somewhere. Thus if
// the name for one of those numbers changed because
// a value were added to the middle, that could cause
// the wrong plugin types to be read from storage
// for a given underlying number. Example of the problem
// here: https://play.golang.org/p/YAaPw5ww3er
const (
	PluginTypeUnknown PluginType = iota
	PluginTypeCredential
	PluginTypeDatabase
	PluginTypeSecrets
)

func (p PluginType) String() string {
	switch p {
	case PluginTypeUnknown:
		return "unknown"
	case PluginTypeCredential:
		return "auth"
	case PluginTypeDatabase:
		return "database"
	case PluginTypeSecrets:
		return "secret"
	default:
		return "unsupported"
	}
}

func ParsePluginType(pluginType string) (PluginType, error) {
	switch pluginType {
	case "unknown":
		return PluginTypeUnknown, nil
	case "auth":
		return PluginTypeCredential, nil
	case "database":
		return PluginTypeDatabase, nil
	case "secret":
		return PluginTypeSecrets, nil
	default:
		return PluginTypeUnknown, fmt.Errorf("%q is not a supported plugin type", pluginType)
	}
}
