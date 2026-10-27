// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package journalfs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// sameVolume rejects a state directory on another volume than the spool:
// the journal must survive with the spool, and fencing moves files by
// rename, which cannot cross volumes.
func sameVolume(spoolDir, stateDir string) error {
	spool, err := deviceOf(spoolDir)
	if err != nil {
		return err
	}
	state, err := deviceOf(stateDir)
	if err != nil {
		return err
	}
	if spool != state {
		return errors.New("journal state directory must be on the same volume as the spool")
	}
	return nil
}

func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat journal volume: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("stat journal volume: no device information")
	}
	return uint64(stat.Dev), nil //nolint:unconvert // Dev is int32 on darwin and uint64 on linux.
}
