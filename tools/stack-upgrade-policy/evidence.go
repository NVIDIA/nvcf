// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func runGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// LatestReleaseTag returns the highest published tag for a stack, for example
// deploy/stacks/self-managed/v1.0.0. Prereleases are skipped: they are not a
// version any customer was told to install, so they are not the baseline a
// release candidate is measured against.
func LatestReleaseTag(root, stackPath string) (string, error) {
	out, err := runGit(root, "tag", "--list", stackPath+"/v*", "--sort=-v:refname")
	if err != nil {
		return "", err
	}
	for _, tag := range strings.Split(out, "\n") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if strings.Contains(tag[strings.LastIndex(tag, "/")+1:], "-") {
			continue
		}
		return tag, nil
	}
	return "", fmt.Errorf("no published release tag under %s", stackPath)
}

// GatherEvidence reports every migration file that changed between a stack's
// last published release and HEAD.
//
// The comparison is against the release tag rather than against the pinned
// migrations image, because the stack does not pin that image: the
// cassandra.migrations.image block in global.yaml.gotmpl only emits a tag when
// an operator supplies one. Until it does, "what landed on main since the last
// stack release" is the closest available answer to what the bundle ships.
func GatherEvidence(root, baseRef string, paths []string) (Evidence, error) {
	if len(paths) == 0 {
		return Evidence{}, nil
	}
	args := append([]string{"diff", "--name-status", baseRef + "..HEAD", "--"}, paths...)
	out, err := runGit(root, args...)
	if err != nil {
		return Evidence{}, err
	}

	var ev Evidence
	add := func(path string, status Status) error {
		if !strings.HasSuffix(path, ".sql") {
			return nil
		}
		if status == Deleted {
			ev.Changes = append(ev.Changes, Change{Path: path, Status: Deleted})
			return nil
		}
		sql, err := runGit(root, "show", "HEAD:"+path)
		if err != nil {
			return err
		}
		ev.Changes = append(ev.Changes, Change{Path: path, Status: status, Class: Classify(sql)})
		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		var err error
		switch fields[0][0] {
		case 'D':
			err = add(fields[1], Deleted)
		case 'R':
			// A rename stops shipping the old path. Recording only the new one
			// would hide a migration being renamed away.
			if len(fields) < 3 {
				continue
			}
			if err = add(fields[1], Deleted); err == nil {
				err = add(fields[2], Modified)
			}
		case 'C':
			// A copy leaves its source in place, so nothing is deleted.
			if len(fields) < 3 {
				continue
			}
			err = add(fields[2], Added)
		case 'A':
			err = add(fields[1], Added)
		case 'M':
			err = add(fields[1], Modified)
		}
		if err != nil {
			return Evidence{}, err
		}
	}
	return ev, nil
}
