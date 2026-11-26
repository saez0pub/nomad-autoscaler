// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// FixtureServer starts a test Nomad API server that answers from the JSON
// files of test-fixtures/<cluster>. It lets tests of this and other packages
// share the same clusters. The server is closed when the test ends.
//
// The files are matched by request path:
//
//	/v1/nodes                      nodes.json
//	/v1/node/<id>                  node-<id>.json
//	/v1/node/<id>/allocations      allocs-<id>.json
//	/v1/evaluations                evaluations.json
//	/v1/job/<id>                   job-<id>.json
//
// Overrides maps a request path to another file of the cluster directory, so
// a test can swap the node list or the evaluations. Requests without a file
// get a 404, which makes a test fail when code reads data it should not.
func FixtureServer(t *testing.T, cluster string, overrides map[string]string) *httptest.Server {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to locate the test fixtures")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "test-fixtures", cluster)

	handler := func(w http.ResponseWriter, r *http.Request) {
		file, ok := overrides[r.URL.Path]
		if !ok {
			file = fixtureFile(r.URL.Path)
		}
		if file == "" {
			http.NotFound(w, r)
			return
		}

		body, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}

	ts := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(ts.Close)
	return ts
}

func fixtureFile(path string) string {
	switch {
	case path == "/v1/nodes":
		return "nodes.json"
	case path == "/v1/evaluations":
		return "evaluations.json"
	case strings.HasPrefix(path, "/v1/job/"):
		return "job-" + strings.TrimPrefix(path, "/v1/job/") + ".json"
	case strings.HasPrefix(path, "/v1/node/") && strings.HasSuffix(path, "/allocations"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/node/"), "/allocations")
		return "allocs-" + id + ".json"
	case strings.HasPrefix(path, "/v1/node/"):
		return "node-" + strings.TrimPrefix(path, "/v1/node/") + ".json"
	default:
		return ""
	}
}
