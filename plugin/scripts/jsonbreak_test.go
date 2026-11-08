package main

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

func TestParseTag(t *testing.T) {
	cases := []struct {
		tag, name string
		opts      []string
		present   bool
	}{
		{`json:"id"`, "id", nil, true},
		{`json:"id,omitempty,string"`, "id", []string{"omitempty", "string"}, true},
		{`json:"-"`, "-", nil, true},
		{`json:"-,"`, "-", []string{""}, true},
		{`json:",omitempty"`, "", []string{"omitempty"}, true},
		{`xml:"id"`, "", nil, false},
		{``, "", nil, false},
	}
	for _, c := range cases {
		name, opts, ok := parseTag(c.tag)
		if name != c.name || ok != c.present || strings.Join(opts, "|") != strings.Join(c.opts, "|") {
			t.Errorf("parseTag(%q) = %q %q %v, want %q %q %v", c.tag, name, opts, ok, c.name, c.opts, c.present)
		}
	}
}

func TestIsValidTag(t *testing.T) {
	for _, s := range []string{"id", "created_at", "a-b", "x.y", "$ref", "@type", "日本"} {
		if !isValidTag(s) {
			t.Errorf("isValidTag(%q) = false", s)
		}
	}
	for _, s := range []string{"", "a\"b", "a,b", "a\\b", "a'b"} {
		if isValidTag(s) {
			t.Errorf("isValidTag(%q) = true", s)
		}
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "warning") != "1 warning" || plural(0, "warning") != "0 warnings" || plural(3, "addition") != "3 additions" {
		t.Error("plural is wrong")
	}
}

func TestSevRank(t *testing.T) {
	if !(sevRank(sevBreak) > sevRank(sevWarn) && sevRank(sevWarn) > sevRank(sevInfo) && sevRank(sevInfo) > sevRank("x")) {
		t.Error("severity order is wrong")
	}
}

func TestUntarRejectsEscapingPaths(t *testing.T) {
	for _, name := range []string{"../evil.go", "/abs/evil.go", "a/../../evil.go"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		body := []byte("package evil\n")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
		tw.Close()
		if err := untar(&buf, t.TempDir()); err == nil {
			t.Errorf("untar accepted %q", name)
		}
	}
}

func TestUntarTruncatedArchive(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := bytes.Repeat([]byte("x"), 4096)
	tw.WriteHeader(&tar.Header{Name: "go.mod", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	cut := buf.Bytes()[:1000]
	if err := untar(bytes.NewReader(cut), t.TempDir()); err == nil {
		t.Error("untar accepted a cut-off archive")
	}
}

func TestRunUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"--old", "a", "--base", "main"},
		{"--old", "a", "--format", "yaml"},
		{"--old", "a", "--fail-on", "sometimes"},
		{"--old", "a", "extra"},
		{"--keys", "--old", "a"},
		{"--nope"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("run(%q) printed a result: %s", args, out.String())
		}
	}
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), version) {
		t.Errorf("--version: %d %q", code, out.String())
	}
}
