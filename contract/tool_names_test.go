package contract

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Nothing in the contract names a tool.
//
// Mendel's server links this module, and no External Tool may be named in
// the server (Mendel's TestNoWrappedToolIsNamedInTheServer, doc 35 §1): a
// tool is data in the registry, and a wrapper holds everything about its
// tool. Here the wrappers are next door, so the tells are read from their
// wrapper.json files rather than kept by hand: each tool's name as a proper
// noun, its slug as a string literal, and its credential names.
func TestNothingInTheContractNamesATool(t *testing.T) {
	wrappers, err := filepath.Glob(filepath.Join("..", "*", "wrapper.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wrappers) == 0 {
		t.Fatal("no wrapper.json beside the contract: run this from the mendel-integrations repository")
	}
	var tells []*regexp.Regexp
	for _, path := range wrappers {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Tool       struct{ Slug, Name string }
			Connection struct{ Credentials []struct{ Name string } }
		}
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		tells = append(tells,
			regexp.MustCompile(`\b`+regexp.QuoteMeta(d.Tool.Name)+`\b`),
			regexp.MustCompile(`"`+regexp.QuoteMeta(d.Tool.Slug)+`"`))
		for _, c := range d.Connection.Credentials {
			tells = append(tells, regexp.MustCompile(`\b`+regexp.QuoteMeta(c.Name)+`\b`))
		}
	}
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, re := range tells {
				if re.MatchString(line) {
					t.Errorf("%s:%d names a wrapped tool (%s); the contract is about every tool\n    %s",
						path, i+1, re, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
