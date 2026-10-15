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
	"gopkg.in/square/go-jose.v2"
	"testing"

	"github.com/go-test/deep"
	"github.com/hashicorp/vault/sdk/logical"
)

const (
	updateRSAKeyBits            = 4096
	updatedRotationPeriod       = "5m0s"
	secondUpdatedRotationPeriod = "1h0m0s"
	updatedTTL                  = "6m0s"
)

func writeConfig(b *backend, storage *logical.Storage, config map[string]interface{}) (*logical.Response, error) {

	req := &logical.Request{
		Operation:  logical.UpdateOperation,
		Path:       "config",
		Storage:    *storage,
		Data:       config,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.IsError() {
		return nil, resp.Error()
	}
	return resp, nil
}

func TestDefaultConfig(t *testing.T) {
	b, storage := getTestBackend(t)

	req := &logical.Request{
		Operation:  logical.ReadOperation,
		Path:       "config",
		Storage:    *storage,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	rotationPeriod := resp.Data[keyRotationDuration].(string)
	tokenTTL := resp.Data[keyTokenTTL].(string)

	if diff := deep.Equal(DefaultKeyRotationPeriod, rotationPeriod); diff != nil {
		t.Error(diff)
	}

	if diff := deep.Equal(DefaultTokenTTL, tokenTTL); diff != nil {
		t.Error(diff)
	}
}

func TestWriteConfig(t *testing.T) {
	b, storage := getTestBackend(t)

	resp, err := writeConfig(b, storage, map[string]interface{}{
		keyRotationDuration: updatedRotationPeriod,
	})
	if err != nil {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	sigAlg := resp.Data[keySignatureAlgorithm].(jose.SignatureAlgorithm)
	rsaKeyBits := resp.Data[keyRSAKeyBits].(int)
	rotationPeriod := resp.Data[keyRotationDuration].(string)
	tokenTTL := resp.Data[keyTokenTTL].(string)
	setIAT := resp.Data[keySetIAT].(bool)
	setJTI := resp.Data[keySetJTI].(bool)
	setNBF := resp.Data[keySetNBF].(bool)

	if diff := deep.Equal(DefaultSignatureAlgorithm, sigAlg); diff != nil {
		t.Error("signature algorithm should be unchanged:", diff)
	}

	if diff := deep.Equal(DefaultRSAKeyBits, rsaKeyBits); diff != nil {
		t.Error("rsa key bits should be unchanged:", diff)
	}

	if diff := deep.Equal(updatedRotationPeriod, rotationPeriod); diff != nil {
		t.Error("failed to update rotation period:", diff)
	}

	if diff := deep.Equal(DefaultTokenTTL, tokenTTL); diff != nil {
		t.Error("expiry period should be unchanged:", diff)
	}

	if diff := deep.Equal(DefaultSetIAT, setIAT); diff != nil {
		t.Error("set_iat should be unchanged:", diff)
	}

	if diff := deep.Equal(DefaultSetJTI, setJTI); diff != nil {
		t.Error("set_jti should be unchanged:", diff)
	}

	if diff := deep.Equal(DefaultSetNBF, setNBF); diff != nil {
		t.Error("set_nbf should be unchanged:", diff)
	}

	resp, err = writeConfig(b, storage, map[string]interface{}{
		keyRSAKeyBits:       updateRSAKeyBits,
		keyRotationDuration: secondUpdatedRotationPeriod,
		keyTokenTTL:         updatedTTL,
		keySetIAT:           false,
		keySetJTI:           false,
		keySetNBF:           false,
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	sigAlg = resp.Data[keySignatureAlgorithm].(jose.SignatureAlgorithm)
	rsaKeyBits = resp.Data[keyRSAKeyBits].(int)
	rotationPeriod = resp.Data[keyRotationDuration].(string)
	tokenTTL = resp.Data[keyTokenTTL].(string)
	setIAT = resp.Data[keySetIAT].(bool)
	setJTI = resp.Data[keySetJTI].(bool)
	setNBF = resp.Data[keySetNBF].(bool)

	if diff := deep.Equal(DefaultSignatureAlgorithm, sigAlg); diff != nil {
		t.Error("signature algorithm should be unchanged:", diff)
	}

	if diff := deep.Equal(updateRSAKeyBits, rsaKeyBits); diff != nil {
		t.Error("failed to update rsa key bits:", diff)
	}

	if diff := deep.Equal(secondUpdatedRotationPeriod, rotationPeriod); diff != nil {
		t.Error("failed to update rotation period:", diff)
	}

	if diff := deep.Equal(updatedTTL, tokenTTL); diff != nil {
		t.Error("expiry period should be unchanged:", diff)
	}

	if diff := deep.Equal(false, setIAT); diff != nil {
		t.Error("expected set_iat to be false")
	}

	if diff := deep.Equal(false, setJTI); diff != nil {
		t.Error("expected set_jti to be false")
	}

	if diff := deep.Equal(false, setNBF); diff != nil {
		t.Error("expected set_nbf to be false")
	}
}

func TestWriteInvalidConfig(t *testing.T) {
	b, storage := getTestBackend(t)

	resp, err := writeConfig(b, storage, map[string]interface{}{
		keyRotationDuration: "not a real duration",
	})
	if err == nil {
		t.Errorf("Should have errored but got response: %#v", resp)
	}

	resp, err = writeConfig(b, storage, map[string]interface{}{
		keyAudiencePattern: "(",
	})
	if err == nil {
		t.Errorf("Should have errored but got response: %#v", resp)
	}

	resp, err = writeConfig(b, storage, map[string]interface{}{
		keyAllowedClaims: []string{"iss"},
	})
	if err == nil {
		t.Errorf("Should have errored but got response: %#v", resp)
	}

	resp, err = writeConfig(b, storage, map[string]interface{}{
		keyAllowedHeaders: []string{"kid"},
	})
	if err == nil {
		t.Errorf("Should have errored but got response: %#v", resp)
	}

	resp, err = writeConfig(b, storage, map[string]interface{}{
		keySignatureAlgorithm: "HS256",
	})
	if err == nil {
		t.Errorf("Should have errored but got response: %#v", resp)
	}
}
