// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"syscall"
	"testing"
)

// Every outcome of a dump whose suspend stopped the source brings the
// source back, but a whole dump of a tree CRIU then killed. A dump that
// succeeded but whose images could not be harvested (no space left to copy
// them) leaves a leave-running source suspended, and is the case that was
// missed: it returned the harvest error with no resume.
func TestFinishDump_SettlesTheSource(t *testing.T) {
	dumpErr, harvestErr, resumeFailed := errors.New("criu dump failed"), syscall.ENOSPC, errors.New("resume failed")
	cases := []struct {
		name                 string
		run, move            error
		suspended, leave     bool
		resume               error
		wantResumes          int
		wantErr, wantLeft    bool
		wantRun, wantHarvest bool
	}{
		{name: "whole leave-running dump", suspended: true, leave: true, wantResumes: 1},
		{name: "whole dump, tree killed", suspended: true, leave: false, wantResumes: 0},
		{name: "harvest fails after a leave-running dump", move: harvestErr, suspended: true, leave: true,
			wantResumes: 1, wantErr: true, wantHarvest: true},
		{name: "harvest fails and the resume fails", move: harvestErr, suspended: true, leave: true, resume: resumeFailed,
			wantResumes: 1, wantErr: true, wantLeft: true, wantHarvest: true},
		{name: "harvest fails, tree killed", move: harvestErr, suspended: true, leave: false,
			wantResumes: 0, wantErr: true, wantHarvest: true},
		{name: "dump fails", run: dumpErr, suspended: true, leave: false,
			wantResumes: 1, wantErr: true, wantRun: true},
		{name: "dump and harvest fail, resume fails", run: dumpErr, move: harvestErr, suspended: true, leave: true, resume: resumeFailed,
			wantResumes: 1, wantErr: true, wantLeft: true, wantRun: true, wantHarvest: true},
		{name: "resume after a whole dump fails", suspended: true, leave: true, resume: resumeFailed,
			wantResumes: 1, wantErr: true, wantLeft: true},
		{name: "nothing suspended", move: harvestErr, leave: true, wantResumes: 0, wantErr: true, wantHarvest: true},
	}
	for _, c := range cases {
		resumes := 0
		err := finishDump(c.run, c.move, []byte("out"), "tail", c.suspended, c.leave, func() error { resumes++; return c.resume })
		if resumes != c.wantResumes {
			t.Errorf("%s: %d resumes, want %d", c.name, resumes, c.wantResumes)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, want error %v", c.name, err, c.wantErr)
			continue
		}
		if got := errors.Is(err, errSourceLeftSuspended); got != c.wantLeft {
			t.Errorf("%s: left suspended = %v, want %v (%v)", c.name, got, c.wantLeft, err)
		}
		if got := errors.Is(err, dumpErr); got != c.wantRun {
			t.Errorf("%s: reaches the dump error = %v, want %v", c.name, got, c.wantRun)
		}
		if got := errors.Is(err, harvestErr); got != c.wantHarvest {
			t.Errorf("%s: reaches the harvest error = %v, want %v", c.name, got, c.wantHarvest)
		}
	}
}

// The pid-namespace dump settles its source the same way.
func TestSettleSource(t *testing.T) {
	for _, c := range []struct {
		suspended, leave, failed bool
		want                     int
	}{
		{true, true, false, 1}, {true, false, false, 0}, {true, false, true, 1}, {true, true, true, 1},
		{false, true, true, 0},
	} {
		n := 0
		if err := settleSource(c.suspended, c.leave, c.failed, func() error { n++; return nil }); err != nil || n != c.want {
			t.Errorf("%+v: %d resumes, err %v; want %d", c, n, err, c.want)
		}
	}
	if err := settleSource(true, true, false, func() error { return errors.New("x") }); !errors.Is(err, errSourceLeftSuspended) {
		t.Errorf("a failed resume reports the source suspended: %v", err)
	}
}

// A capture that fails after the suspend and before its dump (the snapshot
// hook directory cannot be made, the action script cannot be written)
// brings the source back, and tells a group's driver the member failed.
// Once the dump has run its own settlement owns the source, and the guard
// does nothing.
func TestSuspendGuard_SettlesAFailureBeforeTheDump(t *testing.T) {
	prepErr, resumeFailed := syscall.ENOSPC, errors.New("resume failed")
	run := func(arm, handOff bool, resume, prep error) (resumes, aborts int, err error) {
		capture := func() (retErr error) {
			var g suspendGuard
			if arm {
				g.arm(func() error { resumes++; return resume }, func() { aborts++ })
				defer func() { retErr = g.settle(retErr) }()
			}
			if handOff {
				g.handOff()
			}
			return prep
		}
		return resumes, aborts, capture()
	}

	resumes, aborts, err := run(true, false, nil, prepErr)
	if resumes != 1 || aborts != 1 || !errors.Is(err, prepErr) || errors.Is(err, errSourceLeftSuspended) {
		t.Errorf("preparation fails: resumes=%d aborts=%d err=%v; want the source resumed, the group told and the preparation error", resumes, aborts, err)
	}
	resumes, _, err = run(true, false, resumeFailed, prepErr)
	if resumes != 1 || !errors.Is(err, prepErr) || !errors.Is(err, errSourceLeftSuspended) || !errors.Is(err, resumeFailed) {
		t.Errorf("preparation and resume fail: resumes=%d err=%v; want both reported, the source as left suspended", resumes, err)
	}
	if resumes, aborts, err = run(true, true, nil, prepErr); resumes != 0 || aborts != 0 || !errors.Is(err, prepErr) {
		t.Errorf("after the dump: resumes=%d aborts=%d err=%v; the dump's settlement owns the source", resumes, aborts, err)
	}
	if resumes, _, err = run(false, false, nil, prepErr); resumes != 0 || !errors.Is(err, prepErr) {
		t.Errorf("nothing suspended: resumes=%d err=%v", resumes, err)
	}
	// Settled once even if settle runs again.
	var g suspendGuard
	n := 0
	g.arm(func() error { n++; return nil }, nil)
	e := g.settle(g.settle(nil))
	if n != 1 || e != nil {
		t.Errorf("settle twice: %d resumes, err %v; want 1, nil", n, e)
	}
}
