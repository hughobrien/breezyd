// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Grafana dashboard in grafana/breezyd.json queries these metrics. If a metric
// is renamed or dropped the dashboard silently shows nothing, so check that
// every metric name it uses is still declared in this package, and that it
// stays importable (datasource is an input, not a UID from one Grafana).
func TestDashboardMatchesMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../grafana/breezyd.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash map[string]any
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatalf("dashboard is not valid JSON: %v", err)
	}

	// Metric names appear as string literals where they are declared.
	have := map[string]bool{}
	decl := regexp.MustCompile(`"(breezyd?_[a-z0-9_]+)"`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			have[m[1]] = true
		}
	}
	if len(have) == 0 {
		t.Fatal("found no metric declarations; the pattern is stale")
	}

	used := map[string]bool{}
	use := regexp.MustCompile(`\bbreezyd?_[a-z0-9_]+\b`)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if e, ok := x["expr"].(string); ok {
				for _, n := range use.FindAllString(e, -1) {
					used[n] = true
				}
			}
			if uid, ok := x["uid"].(string); ok && x["type"] == "prometheus" && uid != "${DS_PROMETHEUS}" {
				t.Errorf("datasource uid %q is tied to one Grafana; use ${DS_PROMETHEUS}", uid)
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(dash)
	if len(used) == 0 {
		t.Fatal("the dashboard queries no metrics")
	}
	for n := range used {
		base := n
		for _, s := range []string{"_bucket", "_sum", "_count"} {
			if b, ok := strings.CutSuffix(n, s); ok && have[b] {
				base = b
			}
		}
		if !have[base] {
			t.Errorf("dashboard uses %s, which this package does not declare", n)
		}
	}
}
