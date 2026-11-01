/*
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0

Not a contribution
Changes made by NVIDIA CORPORATION & AFFILIATES enabling ESS telemetry, Vault template behavior, and Vault mount/path handling or otherwise documented as
NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
<NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
*/
package dependency

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

// Ensure implements
var _ Dependency = (*VaultReadQuery)(nil)
var _ NvDependency = (*VaultReadQuery)(nil)

// VaultReadQuery is the dependency to Vault for a secret
type VaultReadQuery struct {
	stopCh  chan struct{}
	sleepCh chan time.Duration

	rawPath     string
	queryValues url.Values
	secret      *Secret
	isKVv2      *bool
	secretPath  string

	// vaultSecret is the actual Vault secret which we are renewing
	vaultSecret *api.Secret

	// added for nv
	nv NvVaultQuery
}

// added for nv
type NvVaultReadQueryInput struct {
	SecretUrl                   string
	ErrorOnMissingKey           bool
	RunOnce                     bool
	SkipMountVersionCheck       bool
	ExitOnClientError           bool
	Destination                 string
	TemplateID                  string
	StopProcessingOnClientError bool
}

// added for nv
func NewNvVaultReadQuery(i *NvVaultReadQueryInput) (*VaultReadQuery, error) {
	query, err := NewVaultReadQuery(i.SecretUrl)
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

// NewVaultReadQuery creates a new datacenter dependency.
func NewVaultReadQuery(s string) (*VaultReadQuery, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	if s == "" {
		return nil, fmt.Errorf("ess.read: invalid format: %q", s)
	}

	secretURL, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	return &VaultReadQuery{
		stopCh:      make(chan struct{}, 1),
		sleepCh:     make(chan time.Duration, 1),
		rawPath:     secretURL.Path,
		queryValues: secretURL.Query(),
	}, nil
}

// Fetch queries the Vault API
func (d *VaultReadQuery) Fetch(clients *ClientSet, opts *QueryOptions,
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

	err := d.fetchSecret(clients, opts)
	if err != nil {
		return nil, nil, errors.Wrap(err, d.String())
	}
	if !vaultSecretRenewable(d.secret) {
		dur := leaseCheckWait(d.secret)
		log.Printf("[TRACE] %s: non-renewable secret, set sleep for %s", d, dur)
		d.sleepCh <- dur
	}
	return respWithMetadata(d.secret)
}

func (d *VaultReadQuery) fetchSecret(clients *ClientSet, opts *QueryOptions,
) error {
	opts = opts.Merge(&QueryOptions{})
	vaultSecret, err := d.readSecret(clients, opts)
	if err == nil {
		printVaultWarnings(d, vaultSecret.Warnings)
		d.vaultSecret = vaultSecret
		// the cloned secret which will be exposed to the template
		d.secret = transformSecret(vaultSecret)
	}
	return err
}

func (d *VaultReadQuery) stopChan() chan struct{} {
	return d.stopCh
}

func (d *VaultReadQuery) secrets() (*Secret, *api.Secret) {
	return d.secret, d.vaultSecret
}

// CanShare returns if this dependency is shareable.
func (d *VaultReadQuery) CanShare() bool {
	return false
}

// Stop halts the given dependency's fetch.
func (d *VaultReadQuery) Stop() {
	close(d.stopCh)
}

// String returns the human-friendly version of this dependency.
func (d *VaultReadQuery) String() string {
	if v := d.queryValues["version"]; len(v) > 0 {
		return fmt.Sprintf("ess.read(%s.v%s)", d.rawPath, v[0])
	}
	return fmt.Sprintf("ess.read(%s)", d.rawPath)
}

// Type returns the type of this dependency.
func (d *VaultReadQuery) Type() Type {
	return TypeVault
}

func (d *VaultReadQuery) IsRunOnce() bool {
	return d.nv.runOnce
}

// added for nv
// TemplateID captures the template associated with the dependency
func (d *VaultReadQuery) TemplateID() string {
	return d.nv.templateID
}

func (d *VaultReadQuery) readSecret(clients *ClientSet, opts *QueryOptions) (*api.Secret, error) {
	vaultClient := clients.Vault()

	// added for nv
	// add skipMountVersionCheck condition
	//
	// Check whether this secret refers to a KV v2 entry if we haven't yet.
	if d.nv.skipMountVersionCheck {
		d.secretPath = d.rawPath
	} else if d.isKVv2 == nil {
		mountPath, isKVv2, err := isKVv2(vaultClient, d.rawPath)
		if err != nil {
			// added for nv
			// return err, in the event the sys/internal/ui/mounts response contains a [400,404] status code
			// and exit_on_client_error is enabled
			if isNvClientError(d.nv, err) {
				return nil, NewNvClientErrorWithError(d.rawPath, err)
			}

			// added for nv
			// in the event the sys/internal/ui/mounts response contains status code 404 return error
			if e, ok := err.(*api.ResponseError); ok && e.StatusCode == http.StatusNotFound {
				log.Printf("[ERR] %s: encountered status code %d from path %s : %s", d, e.StatusCode, d.rawPath, err)
				return nil, shimNvClientError(d.nv, d.rawPath,
					NewNvNoSecretAtPathErrorWithError(d.rawPath, d.nv.errorOnMissingKeyEnabled, err))
			}

			log.Printf("[WARN] %s: failed to check if %s is KVv2, "+
				"assume not: %s", d, d.rawPath, err)
			isKVv2 = false
			d.secretPath = d.rawPath
		} else if isKVv2 {
			d.secretPath = shimKVv2Path(d.rawPath, mountPath)
		} else {
			d.secretPath = d.rawPath
		}
		d.isKVv2 = &isKVv2
	}

	queryString := d.queryValues.Encode()
	log.Printf("[TRACE] %s: GET %s", d, &url.URL{
		Path:     "/v1/" + d.secretPath,
		RawQuery: queryString,
	})
	vaultSecret, err := vaultClient.Logical().ReadWithData(d.secretPath,
		d.queryValues)
	if err != nil {
		// added for nv
		return nil, shimNvClientError(d.nv, d.secretPath, errors.Wrap(err, d.String()))
	}
	if vaultSecret == nil || deletedKVv2(vaultSecret) {
		return nil, shimNvClientError(d.nv, d.secretPath, NewNvNoSecretAtPathError(d.secretPath, d.nv.errorOnMissingKeyEnabled))
	}
	return vaultSecret, nil
}

func deletedKVv2(s *api.Secret) bool {
	switch md := s.Data["metadata"].(type) {
	case map[string]interface{}:
		deletionTime, ok := md["deletion_time"].(string)
		if !ok {
			// Key not present or not a string, end early
			return false
		}
		t, err := time.Parse(time.RFC3339, deletionTime)
		if err != nil {
			// Deletion time is either empty, or not a valid string.
			return false
		}

		// If now is 'after' the deletion time, then the secret
		// should be deleted.
		return time.Now().After(t)
	}
	return false
}

// shimKVv2Path aligns the supported legacy path to KV v2 specs by inserting
// /data/ into the path for reading secrets. Paths for metadata are not modified.
func shimKVv2Path(rawPath, mountPath string) string {
	switch {
	case rawPath == mountPath, rawPath == strings.TrimSuffix(mountPath, "/"):
		return path.Join(mountPath, "data")
	default:
		p := strings.TrimPrefix(rawPath, mountPath)

		// Only add /data/ prefix to the path if neither /data/ or /metadata/ are
		// present.
		if strings.HasPrefix(p, "data/") || strings.HasPrefix(p, "metadata/") {
			return rawPath
		}
		return path.Join(mountPath, "data", p)
	}
}
