//
// Copyright 2021 Outfox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package jwtsecrets

import (
	"context"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const (
	jwtSecretsTokenType = "jwt_token"
)

func (b *backend) token() *framework.Secret {
	return &framework.Secret{
		Type: jwtSecretsTokenType,
		Fields: map[string]*framework.FieldSchema{
			"token": {
				Type:        framework.TypeString,
				Description: "Signed JWT",
			},
		},
		Revoke: tokenRevoke,
	}
}

func tokenRevoke(_ context.Context, _ *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	// Nothing to do!
	return nil, nil
}
