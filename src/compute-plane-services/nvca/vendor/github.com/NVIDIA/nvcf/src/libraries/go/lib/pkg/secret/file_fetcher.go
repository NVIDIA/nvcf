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

package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/fsnotify/fsnotify"
)

// Fetcher represents a struct that can fetch file data for clients
type Fetcher interface {
	// FetchData returns cached file data if valid,
	// otherwise it tries to fetch new data and update cache first.
	FetchData(ctx context.Context) (string, error)
}

type FileFetcherOption func(*FileFetcher)

type FileFetcher struct {
	mu sync.RWMutex

	filePath            string
	fileMaxSize         int64
	allowInitialFailure bool
	createDir           bool

	fileRefreshInterval time.Duration

	fileData               []byte
	lastFileModTime        time.Time
	lastSuccessfulFileRead time.Time
	fileErr                error
	nowFunc                func() time.Time
	fileUpdateFunc         func(context.Context, io.Reader)

	// ignoreFileWatcherEvents for testing the interval timer
	ignoreFileWatcherEvents bool
}

func WithMaxFileSize(msize int64) FileFetcherOption {
	return func(ff *FileFetcher) {
		ff.fileMaxSize = msize
	}
}

// WithForceFileRefreshInterval enables a ticker to force refresh
// the file at a particular cadence
func WithForceFileRefreshInterval(fileRefreshInterval time.Duration) FileFetcherOption {
	return func(ff *FileFetcher) {
		if fileRefreshInterval > 0 {
			ff.fileRefreshInterval = fileRefreshInterval
		}
	}
}

// WithOnFileUpdateListener sets a callback function to be called when the file is updated
func WithOnFileUpdateListener(f func(context.Context, io.Reader)) FileFetcherOption {
	return func(ff *FileFetcher) {
		ff.fileUpdateFunc = f
	}
}

// WithAllowInitialFailure allows for ignoring errors from the initial
// fetchAndStoreFileContents() call. Disabled by default.
func WithAllowInitialFailure(allow bool) FileFetcherOption {
	return func(ff *FileFetcher) {
		ff.allowInitialFailure = allow
	}
}

// WithCreateDirIfNotExist attempts to create the directory for the file path
// provided in order to watch the dir when the file doesn't exist. The dir
// is created with 0755 permissions. Disabled by default.
func WithCreateDirIfNotExist(create bool) FileFetcherOption {
	return func(ff *FileFetcher) {
		ff.createDir = create
	}
}

func NewFileFetcher(ctx context.Context, filePath string, opts ...FileFetcherOption) (*FileFetcher, error) {
	if filePath == "" {
		return nil, fmt.Errorf("file path is required")
	}

	fetcher := newFileFetcher()
	fetcher.filePath = filePath
	// Set options on fetcher if any
	for _, o := range opts {
		o(fetcher)
	}

	if err := fetcher.startFetchFromFile(ctx); err != nil {
		return nil, err
	}

	return fetcher, nil
}

func newFileFetcher() *FileFetcher {
	return &FileFetcher{
		fileMaxSize:         1024,
		fileRefreshInterval: time.Minute,
		nowFunc: func() time.Time { //nolint:gocritic
			return time.Now()
		},
		fileUpdateFunc: func(_ context.Context, _ io.Reader) {},
	}
}

func (fetcher *FileFetcher) startFetchFromFile(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Perform the first read of the file to get the contents
	err := fetcher.fetchAndStoreFileContents(ctx, fsnotify.Event{})
	if err != nil {
		if fetcher.allowInitialFailure {
			log.WithError(err).Debugf("fetching file %s initially failed, but continuing to watch directory", fetcher.filePath)
		} else {
			return err
		}
	}

	// Successfully opened the file the first time so open a watcher
	// to the file
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.WithError(err).Errorf("failed to create watcher")
		return err
	}

	watchDirPath := filepath.Dir(fetcher.filePath)
	if fetcher.createDir {
		if err := os.MkdirAll(watchDirPath, 0755); err == nil {
			if err := os.Chmod(watchDirPath, 0755); err != nil {
				log.WithError(err).Errorf("failed to chmod new watch dir path %s", watchDirPath)
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			log.WithError(err).Errorf("failed to create new watch dir path %s", watchDirPath)
		}
	}

	// Must watch the directory, not the individual file due
	// to renaming the file causing the notification to fail to track
	// the old file since it is now a new file
	// https://github.com/fsnotify/fsnotify?tab=readme-ov-file#watching-a-file-doesnt-work-well
	log.Debugf("attempting to create watcher for directory %s", watchDirPath)
	if err := watcher.Add(watchDirPath); err != nil {
		log.WithError(err).Errorf("failed to create watcher for directory %s", watchDirPath)
		watcher.Close()
		return err
	}

	// TODO(mcamp) we should have the liveness probe verify that this
	// is still pulling the file in and reading it correctly by checking the last success
	// vs the last time we attempted a read. Work for a future day.
	go func() {
		forceRefreshTicker := time.NewTicker(fetcher.fileRefreshInterval)
		defer forceRefreshTicker.Stop()
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !fetcher.ignoreFileWatcherEvents {
					// Refetch and store API File contents if necessary
					if ok {
						err = fetcher.fetchAndStoreFileContents(ctx, event)
						if err != nil {
							log.WithError(err).Errorf("failed to retrieve data from file %s", fetcher.filePath)
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !fetcher.ignoreFileWatcherEvents {
					if ok {
						log.WithError(err).Errorf("failure watching file %s", fetcher.filePath)
					}
				}
			case <-forceRefreshTicker.C:
				err := fetcher.fetchAndStoreFileContents(ctx, fsnotify.Event{})
				if err != nil {
					log.WithError(err).Errorf("failed to retrieve data from file %s", fetcher.filePath)
				}
			case <-ctx.Done():
				log.Infof("shutting down file watcher for path %s", fetcher.filePath)
				return
			}
		}
	}()

	return nil
}

func (fetcher *FileFetcher) fetchAndStoreFileContents(ctx context.Context, event fsnotify.Event) error {
	log := core.GetLogger(ctx)
	fInfo, err := os.Stat(fetcher.filePath)
	if err != nil {
		log.WithError(err).Errorf("failed to read fetcher file %s", fetcher.filePath)
		return err
	}

	// If the file was modified re-check it or if it was an empty event (forced by ticker)
	if event.Name == "" ||
		(event.Name == fetcher.filePath && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create))) {
		retries := 2
		for i := 0; i <= retries; i++ {
			// this is a retry sleep for a 10 milliseconds
			if i > 0 {
				time.Sleep(10 * time.Millisecond)
			}
			f, err := os.Open(fetcher.filePath)
			if err != nil {
				log.WithError(err).Errorf("failed to open fetcher file %s", fetcher.filePath)
				fetcher.mu.Lock()
				fetcher.fileErr = err
				fetcher.mu.Unlock()
				return err
			}
			fLimitReader := io.LimitReader(f, fetcher.fileMaxSize)
			b, err := io.ReadAll(fLimitReader)
			if err != nil {
				log.WithError(err).Errorf("failed to read fetcher file %s", fetcher.filePath)
				f.Close()
				fetcher.mu.Lock()
				fetcher.fileErr = err
				fetcher.mu.Unlock()
				return err
			}
			f.Close()

			// read an empty file, and this is not the
			// the last time, if it is the last time
			// allow the empty string to be stored
			if len(b) == 0 && i+1 <= retries {
				continue
			}

			fetcher.mu.Lock()
			fetcher.lastFileModTime = fInfo.ModTime()
			fetcher.lastSuccessfulFileRead = fetcher.nowFunc()
			fetcher.fileData = b
			fetcher.fileErr = nil
			fetcher.mu.Unlock()

			// Create a new reader for the callback
			fetcher.fileUpdateFunc(ctx, bytes.NewReader(b))
			break
		}
	}

	return nil
}

// FetchData fetches the data stored in the fetcher
func (fetcher *FileFetcher) FetchData(_ context.Context) ([]byte, error) {
	fetcher.mu.RLock()
	defer fetcher.mu.RUnlock()
	return fetcher.fileData, fetcher.fileErr
}
