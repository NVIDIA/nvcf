/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	goctrregistry "github.com/google/go-containerregistry/pkg/registry"
)

var tgzChartFileRe = regexp.MustCompile(`^(.+)-([0-9]+\.[0-9]+\.[0-9]+)\.tgz$`)

func NewTestHelmRepoServer(golog *log.Logger, addr, testdataDir, password string) (*http.Server, error) {
	mux := &http.ServeMux{}

	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		gotUsername, gotPassword, ok := r.BasicAuth()
		if !ok {
			w.WriteHeader(401)
			return false
		}
		if gotUsername != "$oauthtoken" || gotPassword != password {
			w.WriteHeader(403)
			return false
		}
		return true
	}

	chartTemplate := template.Must(template.New("").Parse(`
apiVersion: v1
generated: ` + time.Now().Format(time.RFC3339Nano) + `
entries:
  {{- range $i, $chart := . }}
  {{$chart.Name}}:
  - created: {{$chart.Created}}
    digest: {{$chart.Digest}}
    name: {{$chart.Name}}
    urls:
    - http://` + addr + `/{{$chart.Name}}-{{$chart.Version}}.tgz
    version: {{$chart.Version}}
  {{- end }}
`))

	type chart struct {
		Name    string
		Version string
		Digest  string
		Created string
	}
	dirEntries, err := os.ReadDir(testdataDir)
	if err != nil {
		return nil, err
	}
	var charts []chart
	for _, dirEntry := range dirEntries {
		chartName := filepath.Clean(dirEntry.Name())

		if strings.HasSuffix(chartName, ".tgz") {
			golog.Println("Adding chart tgz:", chartName)

			matches := tgzChartFileRe.FindStringSubmatch(chartName)
			if len(matches) != 3 {
				return nil, fmt.Errorf("expected 3 matches on %s, got: %+q", chartName, matches)
			}
			chartPath := filepath.Join(testdataDir, chartName)
			b, err := os.ReadFile(chartPath)
			if err != nil {
				return nil, err
			}
			base := filepath.Base(chartPath)
			mux.HandleFunc(fmt.Sprintf("/%s", base), func(w http.ResponseWriter, r *http.Request) {
				if !checkAuth(w, r) {
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(b)
			})

			dig := sha256.Sum256(b)
			charts = append(charts, chart{
				Name:    matches[1],
				Version: matches[2],
				Digest:  hex.EncodeToString(dig[:]),
				Created: time.Now().Format(time.RFC3339Nano),
			})
			continue
		}

		tbuf := &bytes.Buffer{}
		tw := tar.NewWriter(tbuf)
		err := filepath.Walk(filepath.Join(testdataDir, dirEntry.Name()), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if match, err := filepath.Match("exp*.json", filepath.Base(path)); match || err != nil {
				return err
			}
			linkPath := strings.TrimPrefix(path, "testdata/")
			h, err := tar.FileInfoHeader(info, linkPath)
			if err != nil {
				return err
			}

			h.Name = linkPath
			if err = tw.WriteHeader(h); err != nil {
				return err
			}

			if info.Mode().IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = tw.Close()
		if err != nil {
			return nil, err
		}

		gbuf := &bytes.Buffer{}
		gw := gzip.NewWriter(gbuf)
		_, err = gw.Write(tbuf.Bytes())
		if err != nil {
			return nil, err
		}
		err = gw.Close()
		if err != nil {
			return nil, err
		}

		chartVersion := "1.0.0"
		mux.HandleFunc(fmt.Sprintf("/%s-%s.tgz", chartName, chartVersion), func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(w, r) {
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gbuf.Bytes())
		})

		golog.Println("Adding constructed chart tgz:", chartName, chartVersion)

		dig := sha256.Sum256(gbuf.Bytes())
		charts = append(charts, chart{
			Name:    chartName,
			Version: chartVersion,
			Digest:  hex.EncodeToString(dig[:]),
			Created: time.Now().Format(time.RFC3339Nano),
		})
	}

	ctw := &bytes.Buffer{}
	if err := chartTemplate.Execute(ctw, charts); err != nil {
		return nil, err
	}

	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		_, _ = w.Write(ctw.Bytes())
	})

	srv := &http.Server{
		Handler: mux,
	}

	return srv, nil
}

func NewImageRegistryServer(golog *log.Logger, regHost string) (*http.Server, error) {
	rsrv := goctrregistry.New(goctrregistry.Logger(golog))

	srv := &http.Server{
		Handler: rsrv,
	}

	return srv, nil
}

func PushPublicImages(regHost string, publicImages []string) error {
	for i, tag := range publicImages {
		dst := fmt.Sprintf("%s/%s", regHost, tag)
		ref, err := name.ParseReference(dst)
		if err != nil {
			return err
		}
		img, err := random.Image(1024, 2)
		if err != nil {
			return err
		}
		err = remote.Push(ref, img)
		if err != nil {
			return err
		}

		publicImages[i] = dst
	}
	return nil
}
