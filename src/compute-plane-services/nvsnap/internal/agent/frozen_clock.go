// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/encoding/protowire"
)

// A restored process's clocks continue from the values CRIU read when it
// dumped the process (its time namespace image), so the time it spent
// stopped in between does not count. A group capture holds a pod's
// containers, frozen, from its freeze point until their dump, which waits
// for the GPU ranks' suspend: tens of seconds, minutes for a large model.
// Read at the dump, the clocks count that hold as time the process ran.
// An etcd among the companions then finds, at its restore, every lease
// that was not renewed during the hold expired, and a Dynamo component
// whose lease expires shuts down (seen 2026-10-17: a 60 s lease, a 36 s
// hold). So a held container's dumped clocks are moved back by the time it
// was held frozen before its dump: restored, they continue from the moment
// it stopped.

// clocks is a reading of the clocks a time namespace offsets.
type clocks struct {
	monotonic, boottime time.Duration
}

func readClocks() (clocks, error) {
	var m, b unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &m); err != nil {
		return clocks{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &b); err != nil {
		return clocks{}, err
	}
	return clocks{monotonic: time.Duration(m.Nano()), boottime: time.Duration(b.Nano())}, nil
}

// heldFor is how long each clock ran between at and later.
func (at clocks) heldFor(later clocks) clocks {
	return clocks{monotonic: later.monotonic - at.monotonic, boottime: later.boottime - at.boottime}
}

// shiftTimensImages moves the clocks of every time namespace image in dir
// (timens-<id>.img) back by by, and reports how many it moved.
func shiftTimensImages(dir string, by clocks) (int, error) {
	if by.monotonic <= 0 && by.boottime <= 0 {
		return 0, nil
	}
	paths, err := filepath.Glob(filepath.Join(dir, "timens-*.img"))
	if err != nil {
		return 0, err
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0, err
		}
		nb, err := shiftTimensImage(b, by)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if err := os.WriteFile(p, nb, 0o600); err != nil { //nolint:gosec // p is a glob match in the checkpoint directory
			return 0, err
		}
	}
	return len(paths), nil
}

// shiftTimensImage rewrites a CRIU time namespace image: the image magics,
// then one entry (a little-endian uint32 size and a TimensEntry: field 1
// the monotonic clock, field 2 the boot clock, each a Timespec of tv_sec
// (1) and tv_nsec (2)).
func shiftTimensImage(b []byte, by clocks) ([]byte, error) {
	if len(b) < 12 {
		return nil, errors.New("short time namespace image")
	}
	size := int(binary.LittleEndian.Uint32(b[8:12]))
	if len(b) < 12+size {
		return nil, errors.New("truncated time namespace entry")
	}
	entry := b[12 : 12+size]
	var out []byte
	for len(entry) > 0 {
		num, typ, n := protowire.ConsumeTag(entry)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		entry = entry[n:]
		if typ != protowire.BytesType || (num != 1 && num != 2) {
			m := protowire.ConsumeFieldValue(num, typ, entry)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			out = protowire.AppendTag(out, num, typ)
			out = append(out, entry[:m]...)
			entry = entry[m:]
			continue
		}
		ts, m := protowire.ConsumeBytes(entry)
		if m < 0 {
			return nil, protowire.ParseError(m)
		}
		entry = entry[m:]
		d := by.monotonic
		if num == 2 {
			d = by.boottime
		}
		nts, err := shiftTimespec(ts, d)
		if err != nil {
			return nil, err
		}
		out = protowire.AppendTag(out, num, protowire.BytesType)
		out = protowire.AppendBytes(out, nts)
	}
	nb := append([]byte{}, b[:8]...)
	nb = binary.LittleEndian.AppendUint32(nb, uint32(len(out))) //nolint:gosec // a time namespace entry is a few dozen bytes
	nb = append(nb, out...)
	return append(nb, b[12+size:]...), nil
}

// shiftTimespec moves an encoded Timespec back by d, not below zero.
func shiftTimespec(b []byte, d time.Duration) ([]byte, error) {
	var sec, nsec uint64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		if typ != protowire.VarintType {
			return nil, fmt.Errorf("timespec field %d: not a varint", num)
		}
		v, m := protowire.ConsumeVarint(b)
		if m < 0 {
			return nil, protowire.ParseError(m)
		}
		b = b[m:]
		switch num {
		case 1:
			sec = v
		case 2:
			nsec = v
		}
	}
	t := time.Duration(sec)*time.Second + time.Duration(nsec) - d //nolint:gosec // clock values are far below the overflow
	if t < 0 {
		t = 0
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(t/time.Second))
	out = protowire.AppendTag(out, 2, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(t%time.Second))
	return out, nil
}
