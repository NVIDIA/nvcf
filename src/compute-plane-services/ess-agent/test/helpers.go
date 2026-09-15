/*
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0

Not a contribution
Changes made by NVIDIA CORPORATION & AFFILIATES enabling SIGTERM shutdown behavior or otherwise documented as
NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
<NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
*/
package test

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CreateTempfile that will be closed and removed at the end of the test.
func CreateTempfile(tb testing.TB, b []byte) *os.File {
	tb.Helper()

	f, err := os.CreateTemp(os.TempDir(), "")
	require.NoError(tb, err)

	tb.Cleanup(func() {
		fName := f.Name()
		assert.NoError(tb, f.Close())
		assert.NoError(tb, os.Remove(fName))
	})

	if len(b) > 0 {
		_, err = f.Write(b)
		assert.NoError(tb, err)
	}

	return f
}

func DeleteTempfile(f *os.File, t *testing.T) {
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
}

func WaitForContents(t *testing.T, d time.Duration, p, c string) {
	errCh := make(chan error, 1)
	matchCh := make(chan struct{}, 1)
	stopCh := make(chan struct{}, 1)
	var last string

	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
			}

			actual, err := os.ReadFile(p)
			if err != nil && !os.IsNotExist(err) {
				errCh <- err
				return
			}

			last = string(actual)
			if strings.EqualFold(last, c) {
				close(matchCh)
				return
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-matchCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(d):
		close(stopCh)
		t.Errorf("contents not present after %s, expected: %q, actual: %q",
			d, c, last)
	}
}

// TestingTB implements testutil.TestingTB interface
var _ testutil.TestingTB = (*TestingTB)(nil)

type TestingTB struct {
	cleanup func()
	sync.Mutex
}

func (t *TestingTB) DoCleanup() {
	t.Lock()
	defer t.Unlock()
	t.cleanup()
}

func (*TestingTB) Failed() bool                  { return false }
func (*TestingTB) Fail()                         {}
func (*TestingTB) FailNow()                      {}
func (*TestingTB) Fatal(...interface{})          {}
func (*TestingTB) Log(...interface{})            {}
func (*TestingTB) Logf(string, ...interface{})   {}
func (*TestingTB) Fatalf(string, ...interface{}) {}
func (*TestingTB) Error(...interface{})          {}
func (*TestingTB) Errorf(string, ...interface{}) {}
func (*TestingTB) Name() string                  { return "TestingTB" }
func (*TestingTB) Helper()                       {}
func (*TestingTB) Setenv(key, value string)      {}
func (*TestingTB) TempDir() string               { return "/tmp" }
func (t *TestingTB) Cleanup(f func()) {
	t.Lock()
	defer t.Unlock()
	prev := t.cleanup
	t.cleanup = func() {
		f()
		if prev != nil {
			prev()
		}
	}
}
