// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// A time namespace image as CRIU wrote it for an etcd companion.
var timensImage = []byte{
	0x19, 0x43, 0x56, 0x54, 0x33, 0x44, 0x11, 0x43, 0x16, 0x00, 0x00, 0x00,
	0x0a, 0x09, 0x08, 0x9e, 0xca, 0x7f, 0x10, 0x9a, 0xbf, 0xaf, 0x60,
	0x12, 0x09, 0x08, 0x9e, 0xca, 0x7f, 0x10, 0xba, 0xbf, 0xaf, 0x60,
}

// readTimens decodes the monotonic and boot clocks of a time namespace image.
func readTimens(t *testing.T, b []byte) clocks {
	t.Helper()
	if !bytes.Equal(b[:8], timensImage[:8]) {
		t.Fatalf("image magics changed: % x", b[:8])
	}
	size := int(binary.LittleEndian.Uint32(b[8:12]))
	if len(b) != 12+size {
		t.Fatalf("entry size %d, image %d bytes", size, len(b))
	}
	var c clocks
	e := b[12:]
	for len(e) > 0 {
		num, _, n := protowire.ConsumeTag(e)
		e = e[n:]
		ts, m := protowire.ConsumeBytes(e)
		e = e[m:]
		var d time.Duration
		for len(ts) > 0 {
			f, _, k := protowire.ConsumeTag(ts)
			ts = ts[k:]
			v, l := protowire.ConsumeVarint(ts)
			ts = ts[l:]
			if f == 1 {
				d += time.Duration(v) * time.Second //nolint:gosec // test values
			} else {
				d += time.Duration(v) //nolint:gosec // test values
			}
		}
		if num == 1 {
			c.monotonic = d
		} else {
			c.boottime = d
		}
	}
	return c
}

// A held container's dumped clocks move back by the hold: restored, they
// continue from when the hold froze it.
func TestShiftTimensImage(t *testing.T) {
	before := readTimens(t, timensImage)
	if before.monotonic < 24*time.Hour || before.boottime < before.monotonic {
		t.Fatalf("fixture decodes to %+v", before)
	}
	by := clocks{monotonic: 36*time.Second + 250*time.Millisecond, boottime: 36*time.Second + 300*time.Millisecond}
	b, err := shiftTimensImage(timensImage, by)
	if err != nil {
		t.Fatal(err)
	}
	after := readTimens(t, b)
	if got := before.monotonic - after.monotonic; got != by.monotonic {
		t.Errorf("monotonic moved %s, want %s", got, by.monotonic)
	}
	if got := before.boottime - after.boottime; got != by.boottime {
		t.Errorf("boottime moved %s, want %s", got, by.boottime)
	}
}

// A clock is never moved below zero.
func TestShiftTimespecClamps(t *testing.T) {
	var ts []byte
	ts = protowire.AppendTag(ts, 1, protowire.VarintType)
	ts = protowire.AppendVarint(ts, 5)
	out, err := shiftTimespec(ts, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := protowire.ConsumeVarint(out[1:])
	if v != 0 {
		t.Errorf("tv_sec = %d, want 0", v)
	}
}

// Every time namespace image of a checkpoint is moved; other images and a
// zero hold are left alone.
func TestShiftTimensImages(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"timens-0.img", "inventory.img"} {
		if err := os.WriteFile(filepath.Join(dir, f), timensImage, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := shiftTimensImages(dir, clocks{}); n != 0 || err != nil {
		t.Fatalf("zero hold: %d, %v", n, err)
	}
	by := clocks{monotonic: 10 * time.Second, boottime: 10 * time.Second}
	n, err := shiftTimensImages(dir, by)
	if n != 1 || err != nil {
		t.Fatalf("got %d, %v; want one image moved", n, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "timens-0.img"))
	if readTimens(t, timensImage).monotonic-readTimens(t, b).monotonic != 10*time.Second {
		t.Error("timens-0.img not moved by the hold")
	}
	other, _ := os.ReadFile(filepath.Join(dir, "inventory.img"))
	if !bytes.Equal(other, timensImage) {
		t.Error("an image other than the time namespace's was changed")
	}
	if _, err := shiftTimensImage(timensImage[:10], by); err == nil {
		t.Error("a short image was accepted")
	}
}

func TestHeldFor(t *testing.T) {
	at := clocks{monotonic: time.Second, boottime: 2 * time.Second}
	got := at.heldFor(clocks{monotonic: 4 * time.Second, boottime: 6 * time.Second})
	if got != (clocks{monotonic: 3 * time.Second, boottime: 4 * time.Second}) {
		t.Errorf("heldFor = %+v", got)
	}
	if c, err := readClocks(); err != nil || c.monotonic <= 0 || c.boottime < c.monotonic {
		t.Errorf("readClocks = %+v, %v", c, err)
	}
}
