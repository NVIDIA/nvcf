// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Env aliases let a module that links this package accept the environment
// names an existing deployment already sets, so switching uploaders needs no
// change to that deployment. The module registers them from an init function,
// as backends do, and the uploader reads its settings through WithAliases.

var (
	aliasMu sync.RWMutex
	// aliasesBySetting lists each setting's aliases in registration order.
	aliasesBySetting = map[string][]string{}
	// aliasOwner maps each alias to the one setting it stands for.
	aliasOwner = map[string]string{}
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// knownSettings is the set of names Load reads, found by running Load once
// with a recording lookup, so it cannot drift from Load.
var knownSettings = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	_, _, _ = Load(func(name string) (string, bool) {
		names[name] = true
		switch name {
		case EnvSourceDir:
			return "/", true
		case EnvBackend:
			return string(BackendDebug), true
		}
		return "", false
	})
	return names
})

// RegisterEnvAlias makes alias another environment name for setting, one of
// the Env constants. When several aliases are registered for one setting, the
// first one set wins. Misuse panics, because it can only be a build wiring
// mistake: an unknown setting, an invalid name, an alias that is itself a
// setting name, or an alias registered twice.
func RegisterEnvAlias(setting, alias string) {
	if !knownSettings()[setting] {
		panic(fmt.Sprintf("env alias %q registered for unknown setting %q", alias, setting))
	}
	if !envNamePattern.MatchString(alias) {
		panic(fmt.Sprintf("env alias %q for %s is not a valid environment name", alias, setting))
	}
	if knownSettings()[alias] {
		panic(fmt.Sprintf("env alias %q is already a setting name", alias))
	}
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if owner, exists := aliasOwner[alias]; exists {
		panic(fmt.Sprintf("env alias %q registered twice (already an alias of %s)", alias, owner))
	}
	aliasOwner[alias] = setting
	aliasesBySetting[setting] = append(aliasesBySetting[setting], alias)
}

// WithAliases wraps lookup so that a setting with no value of its own takes
// the value of its first set alias. A blank value counts as unset, as it does
// in Load. Two set aliases of one setting with different values are an error
// unless the setting's own name is set, which always wins.
func WithAliases(lookup LookupFunc) (LookupFunc, error) {
	if lookup == nil {
		return nil, errors.New("environment lookup is required")
	}
	aliasMu.RLock()
	aliases := make(map[string][]string, len(aliasesBySetting))
	for setting, names := range aliasesBySetting {
		aliases[setting] = append([]string(nil), names...)
	}
	aliasMu.RUnlock()

	resolved := make(map[string]string)
	var conflicts []error
	for setting, names := range aliases {
		if _, ok := nonBlank(lookup, setting); ok {
			continue
		}
		var chosen, chosenFrom string
		for _, name := range names {
			value, ok := nonBlank(lookup, name)
			if !ok {
				continue
			}
			if chosenFrom == "" {
				chosen, chosenFrom = value, name
				continue
			}
			if value != chosen {
				conflicts = append(conflicts, fmt.Errorf("%s and %s set different values for %s", chosenFrom, name, setting))
			}
		}
		if chosenFrom != "" {
			resolved[setting] = chosen
		}
	}
	if len(conflicts) > 0 {
		return nil, errors.Join(conflicts...)
	}
	return func(name string) (string, bool) {
		if value, ok := resolved[name]; ok {
			return value, true
		}
		return lookup(name)
	}, nil
}

func nonBlank(lookup LookupFunc, name string) (string, bool) {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}
