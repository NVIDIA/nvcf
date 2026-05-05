/*
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0

Not a contribution
Changes made by NVIDIA CORPORATION & AFFILIATES enabling ESS telemetry and Vault template behavior or otherwise documented as
NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
<NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
*/
package dependency

import (
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

// Ensure implements
var _ Dependency = (*VaultWriteQuery)(nil)
var _ NvDependency = (*VaultWriteQuery)(nil)

// VaultWriteQuery is the dependency to Vault for a secret
type VaultWriteQuery struct {
	stopCh  chan struct{}
	sleepCh chan time.Duration

	path     string
	data     map[string]interface{}
	dataHash string
	secret   *Secret

	// vaultSecret is the actual Vault secret which we are renewing
	vaultSecret *api.Secret

	// added for nv
	nv NvVaultQuery
}

// added for nv
type NvVaultWriteQueryInput struct {
	SecretUrl                   string
	Data                        map[string]interface{}
	ErrorOnMissingKey           bool
	RunOnce                     bool
	SkipMountVersionCheck       bool
	Destination                 string
	TemplateID                  string
	ExitOnClientError           bool
	StopProcessingOnClientError bool
}

// added for nv
func NewNvVaultWriteQuery(i *NvVaultWriteQueryInput) (*VaultWriteQuery, error) {
	query, err := NewVaultWriteQuery(i.SecretUrl, i.Data)
	if err != nil {
		return nil, err
	}
	query.nv.errorOnMissingKeyEnabled = i.ErrorOnMissingKey
	query.nv.runOnce = i.RunOnce
	query.nv.skipMountVersionCheck = i.SkipMountVersionCheck
	query.nv.exitOnClientError = i.ExitOnClientError
	query.nv.stopProcessingOnClientError = i.StopProcessingOnClientError
	query.nv.destination = i.Destination
	query.nv.templateID = i.TemplateID
	return query, nil
}

// NewVaultWriteQuery creates a new datacenter dependency.
func NewVaultWriteQuery(s string, d map[string]interface{}) (*VaultWriteQuery, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	if s == "" {
		return nil, fmt.Errorf("ess.write: invalid format: %q", s)
	}

	return &VaultWriteQuery{
		stopCh:   make(chan struct{}, 1),
		sleepCh:  make(chan time.Duration, 1),
		path:     s,
		data:     d,
		dataHash: sha1Map(d),
	}, nil
}

// Fetch queries the Vault API
func (d *VaultWriteQuery) Fetch(clients *ClientSet, opts *QueryOptions,
) (interface{}, *ResponseMetadata, error) {
	select {
	case <-d.stopCh:
		return nil, nil, ErrStopped
	default:
	}
	select {
	case dur := <-d.sleepCh:
		// added for nv
		// check stop channel after sleep to see if fetch loop should stop
		if !d.nv.runOnce {
			log.Printf("[INFO] %s next renewal in %q", d.String(), dur)
		}
		time.Sleep(dur)
		select {
		case <-d.stopCh:
			return nil, nil, ErrStopped
		default:
		}
	default:
	}

	firstRun := d.secret == nil

	if !firstRun && vaultSecretRenewable(d.secret) {
		err := renewSecret(clients, d)
		if err != nil {
			return nil, nil, errors.Wrap(err, d.String())
		}
	}

	opts = opts.Merge(&QueryOptions{})
	vaultSecret, err := d.writeSecret(clients, opts)
	if err != nil {
		return nil, nil, errors.Wrap(err, d.String())
	}

	// vaultSecret == nil when writing to KVv1 engines
	if vaultSecret == nil {
		return respWithMetadata(d.secret)
	}

	printVaultWarnings(d, vaultSecret.Warnings)
	d.vaultSecret = vaultSecret
	// cloned secret which will be exposed to the template
	d.secret = transformSecret(vaultSecret)

	if !vaultSecretRenewable(d.secret) {
		dur := leaseCheckWait(d.secret)
		log.Printf("[TRACE] %s: non-renewable secret, set sleep for %s", d, dur)
		d.sleepCh <- dur
	}

	return respWithMetadata(d.secret)
}

// meet renewer interface
func (d *VaultWriteQuery) stopChan() chan struct{} {
	return d.stopCh
}

func (d *VaultWriteQuery) secrets() (*Secret, *api.Secret) {
	return d.secret, d.vaultSecret
}

// CanShare returns if this dependency is shareable.
func (d *VaultWriteQuery) CanShare() bool {
	return false
}

// Stop halts the given dependency's fetch.
func (d *VaultWriteQuery) Stop() {
	close(d.stopCh)
}

// String returns the human-friendly version of this dependency.
func (d *VaultWriteQuery) String() string {
	return fmt.Sprintf("ess.write(%s -> %s)", d.path, d.dataHash)
}

// Type returns the type of this dependency.
func (d *VaultWriteQuery) Type() Type {
	return TypeVault
}

func (d *VaultWriteQuery) IsRunOnce() bool {
	return d.nv.runOnce
}

// added for nv
// TemplateID captures the template associated with the dependency
func (d *VaultWriteQuery) TemplateID() string {
	return d.nv.templateID
}

// sha1Map returns the sha1 hash of the data in the map. The reason this data is
// hashed is because it appears in the output and could contain sensitive
// information.
func sha1Map(m map[string]interface{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha1.New()
	for _, k := range keys {
		io.WriteString(h, fmt.Sprintf("%s=%q", k, m[k]))
	}

	return fmt.Sprintf("%.4x", h.Sum(nil))
}

func (d *VaultWriteQuery) printWarnings(warnings []string) {
	for _, w := range warnings {
		log.Printf("[WARN] %s: %s", d, w)
	}
}

func (d *VaultWriteQuery) writeSecret(clients *ClientSet, opts *QueryOptions) (*api.Secret, error) {
	log.Printf("[TRACE] %s: PUT %s", d, &url.URL{
		Path:     "/v1/" + d.path,
		RawQuery: opts.String(),
	})

	path := d.path
	data := d.data

	// added for nv
	// add skipMountVersionCheck condition
	var isv2 bool
	if !d.nv.skipMountVersionCheck {
		var mountPath string
		mountPath, isv2, _ = isKVv2(clients.Vault(), path)
		if isv2 {
			path = shimKVv2Path(path, mountPath)
			data = map[string]interface{}{"data": d.data}
		}
	}

	vaultSecret, err := clients.Vault().Logical().Write(path, data)
	if err != nil {
		// added for nv
		return nil, shimNvClientError(d.nv, d.path, errors.Wrap(err, d.String()))
	}
	// vaultSecret is always nil when KVv1 engine (isv2==false)
	//
	// added for nv
	// when skipMountVersionCheck is true, do not check if secret is nil;
	// it cannot be determined if path is a KV v2 path due to the skipped check
	if !d.nv.skipMountVersionCheck {
		if isv2 && vaultSecret == nil {
			return nil, shimNvClientError(d.nv, d.path, NewNvNoSecretAtPathError(d.path, d.nv.errorOnMissingKeyEnabled))
		}
	}

	return vaultSecret, nil
}
