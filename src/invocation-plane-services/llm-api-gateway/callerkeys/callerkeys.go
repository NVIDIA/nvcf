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

// Package callerkeys authenticates gateway callers against static API keys
// when the gateway runs without the NVCF control plane.
package callerkeys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Entry is one caller key as a store holds it: a name and the hex SHA-256 of
// the plain key. Stores hold hashes only.
type Entry struct {
	ID     string `yaml:"id"`
	SHA256 string `yaml:"sha256"`
}

// Store supplies caller key entries. The file is the first store; another
// store can replace it without changing how keys are validated or matched.
type Store interface {
	Entries(ctx context.Context) ([]Entry, error)
}

// FileStore reads entries from a YAML file of the form
//
//	keys:
//	  - id: demo-ui
//	    sha256: <64 hex>
type FileStore struct {
	path string
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Entries(_ context.Context) ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read caller key file: %w", err)
	}

	var file struct {
		Keys []Entry `yaml:"keys"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse caller key file %s: %w", s.path, err)
	}
	return file.Keys, nil
}

// KeySet is a validated, in-memory set of caller keys.
type KeySet struct {
	keys []key
}

type key struct {
	id   string
	hash [sha256.Size]byte
}

// Load reads entries from store and validates them into a KeySet.
func Load(ctx context.Context, store Store) (*KeySet, error) {
	entries, err := store.Entries(ctx)
	if err != nil {
		return nil, fmt.Errorf("load caller keys: %w", err)
	}

	keys := make([]key, 0, len(entries))
	seenIDs := make(map[string]struct{}, len(entries))
	seenHashes := make(map[[sha256.Size]byte]struct{}, len(entries))
	for i, entry := range entries {
		// The id becomes the rate-limit key, which must not be empty.
		if entry.ID == "" {
			return nil, fmt.Errorf("caller key %d: id is required", i+1)
		}
		if _, ok := seenIDs[entry.ID]; ok {
			return nil, fmt.Errorf("duplicate caller key id %q", entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}

		var hash [sha256.Size]byte
		if len(entry.SHA256) != hex.EncodedLen(sha256.Size) {
			return nil, fmt.Errorf("caller key %q: sha256 is not 64 hex characters", entry.ID)
		}
		if _, err := hex.Decode(hash[:], []byte(entry.SHA256)); err != nil {
			return nil, fmt.Errorf("caller key %q: sha256 is not 64 hex characters", entry.ID)
		}
		if _, ok := seenHashes[hash]; ok {
			return nil, fmt.Errorf("caller key %q repeats the sha256 of another key", entry.ID)
		}
		seenHashes[hash] = struct{}{}

		keys = append(keys, key{id: entry.ID, hash: hash})
	}
	return &KeySet{keys: keys}, nil
}

// Lookup returns the id of the key whose hash matches the SHA-256 of apiKey.
func (s *KeySet) Lookup(apiKey string) (string, bool) {
	if apiKey == "" {
		return "", false
	}

	hash := sha256.Sum256([]byte(apiKey))
	id, ok := "", false
	for _, k := range s.keys {
		if subtle.ConstantTimeCompare(hash[:], k.hash[:]) == 1 {
			id, ok = k.id, true
		}
	}
	return id, ok
}
