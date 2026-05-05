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

package framework

import (
	"errors"

	"github.com/hashicorp/errwrap"
	"github.com/hashicorp/vault/sdk/helper/identitytpl"
	"github.com/hashicorp/vault/sdk/logical"
)

// PopulateIdentityTemplate takes a template string, an entity ID, and an
// instance of system view. It will query system view for information about the
// entity and use the resulting identity information to populate the template
// string.
func PopulateIdentityTemplate(tpl string, entityID string, sysView logical.SystemView) (string, error) {
	entity, err := sysView.EntityInfo(entityID)
	if err != nil {
		return "", err
	}
	if entity == nil {
		return "", errors.New("no entity found")
	}

	groups, err := sysView.GroupsForEntity(entityID)
	if err != nil {
		return "", err
	}

	input := identitytpl.PopulateStringInput{
		String: tpl,
		Entity: entity,
		Groups: groups,
		Mode:   identitytpl.ACLTemplating,
	}

	_, out, err := identitytpl.PopulateString(input)
	if err != nil {
		return "", err
	}

	return out, nil
}

// ValidateIdentityTemplate takes a template string and returns if the string is
// a valid identity template.
func ValidateIdentityTemplate(tpl string) (bool, error) {
	hasTemplating, _, err := identitytpl.PopulateString(identitytpl.PopulateStringInput{
		Mode:              identitytpl.ACLTemplating,
		ValidityCheckOnly: true,
		String:            tpl,
	})
	if err != nil {
		return false, errwrap.Wrapf("failed to validate policy templating: {{err}}", err)
	}

	return hasTemplating, nil
}
