// Package tests runs the jsonbreak command against real modules, compiled
// by the go toolchain on the test machine.
package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	toolPath  string
	buildErr  error
	buildOut  []byte
)

func tool(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "jsonbreak-tool")
		if err != nil {
			buildErr = err
			return
		}
		exe := ""
		if runtime.GOOS == "windows" {
			exe = ".exe"
		}
		toolPath = filepath.Join(dir, "jsonbreak"+exe)
		cmd := exec.Command("go", "build", "-o", toolPath, "./plugin/scripts")
		cmd.Dir = ".."
		buildOut, buildErr = cmd.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("building jsonbreak: %v\n%s", buildErr, buildOut)
	}
	return toolPath
}

type result struct {
	stdout, stderr string
	code           int
}

func runIn(t *testing.T, dir string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(tool(t), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func runTool(t *testing.T, args ...string) result {
	t.Helper()
	return runIn(t, "", nil, args...)
}

func example(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/m\n\ngo 1.22\n"
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type report struct {
	Compared int `json:"types_compared"`
	Findings []struct {
		Severity string `json:"severity"`
		Type     string `json:"type"`
		Key      string `json:"key"`
		Change   string `json:"change"`
		Message  string `json:"message"`
	} `json:"findings"`
	Breaks   int `json:"breaks"`
	Warnings int `json:"warnings"`
}

func jsonReport(t *testing.T, r result) report {
	t.Helper()
	var rep report
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("output isn't JSON: %v\nstdout: %s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return rep
}

// changes lists findings as "severity type key change", sorted.
func changes(rep report) []string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, strings.TrimSpace(f.Severity+" "+f.Type+" "+f.Key)+" "+f.Change)
	}
	sort.Strings(out)
	return out
}

func TestExampleBreaks(t *testing.T) {
	r := runTool(t, "--old", example(t, "orders-v1"), "--new", example(t, "orders-v2"), "--format", "json")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", r.code, r.stdout, r.stderr)
	}
	got := changes(jsonReport(t, r))
	want := []string{
		"break api.Customer email key-removed",
		"break api.Item sku key-removed",
		"break api.Order customer_id key-renamed",
		"break api.Order quantity string-option",
		"break api.Order shipping.days type-changed",
		"info api.Order currency key-added",
		"warn api.Item price custom-encoding",
		"warn api.Meta created_at now-nullable",
		"warn api.Order note omitempty-added",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, failOn := range []string{"never"} {
		r := runTool(t, "--old", example(t, "orders-v1"), "--new", example(t, "orders-v2"), "--fail-on", failOn)
		if r.code != 0 || !strings.Contains(r.stdout, "5 breaking") {
			t.Errorf("--fail-on %s: exit %d\n%s", failOn, r.code, r.stdout)
		}
	}
}

func TestExampleRefactorStaysQuiet(t *testing.T) {
	for _, failOn := range []string{"break", "warn"} {
		r := runTool(t, "--old", example(t, "orders-v1"), "--new", example(t, "orders-v1-refactor"), "--fail-on", failOn, "--format", "json")
		if r.code != 0 {
			t.Fatalf("--fail-on %s: exit %d\n%s%s", failOn, r.code, r.stdout, r.stderr)
		}
		rep := jsonReport(t, r)
		if rep.Breaks != 0 || rep.Warnings != 0 || rep.Compared != 4 {
			t.Errorf("refactor: %+v", rep)
		}
	}
}

func TestSameRevisionIsQuiet(t *testing.T) {
	for _, ex := range []string{"orders-v1", "orders-v2", "orders-v1-refactor"} {
		r := runTool(t, "--old", example(t, ex), "--new", example(t, ex), "--fail-on", "warn")
		if r.code != 0 || !strings.Contains(r.stdout, "no breaking changes or warnings") {
			t.Errorf("%s against itself: exit %d\n%s%s", ex, r.code, r.stdout, r.stderr)
		}
	}
}

// The tool's own module is a real program with JSON types (its report).
func TestOwnRepoIsQuiet(t *testing.T) {
	repo, _ := filepath.Abs("..")
	r := runTool(t, "--old", repo, "--new", repo, "--pkg", "./plugin/...", "--fail-on", "warn")
	if r.code != 0 || !strings.Contains(r.stdout, "no breaking changes or warnings") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// damage replaces an input with something that can't be trusted.
type damage struct {
	name  string
	apply func(t *testing.T, dir string)
}

func failedBuildOutput(t *testing.T, test bool) []byte {
	t.Helper()
	dir := writeModule(t, map[string]string{"a/a.go": "package a\n\ntype T struct {\n\tX undefinedType `json:\"x\"`\n}\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { t.Fatal(\"boom\") }\n"})
	args := []string{"build", "./..."}
	if test {
		args = []string{"test", "./..."}
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected the build to fail")
	}
	return out
}

func damages(t *testing.T) []damage {
	order := "api/order.go"
	write := func(rel string, data []byte) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	src, err := os.ReadFile(filepath.Join(example(t, "orders-v1"), order))
	if err != nil {
		t.Fatal(err)
	}
	mod, _ := os.ReadFile(filepath.Join(example(t, "orders-v1"), "go.mod"))
	garbage := []byte{0x7f, 'E', 'L', 'F', 0, 1, 2, 0xff, 0xfe, '{', '"', 'x'}
	return []damage{
		{"empty dir", func(t *testing.T, dir string) {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}},
		{"empty go file", write(order, nil)},
		{"garbage go file", write(order, garbage)},
		{"cut-off go file", write(order, src[:len(src)/2])},
		{"go build failure output", write(order, failedBuildOutput(t, false))},
		{"go test failure output", write(order, failedBuildOutput(t, true))},
		{"type error", write(order, bytes.Replace(src, []byte("Count int "), []byte("Count nope"), 1))},
		{"empty go.mod", write("go.mod", nil)},
		{"garbage go.mod", write("go.mod", garbage)},
		{"cut-off go.mod", write("go.mod", mod[:10])},
		{"go.mod is a build log", write("go.mod", failedBuildOutput(t, false))},
		{"no go.mod", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "go.mod")) }},
		{"no json tags", write(order, bytes.ReplaceAll(src, []byte("`json:"), []byte("`xml:")))},
	}
}

func TestDamagedInputNeverPasses(t *testing.T) {
	good := example(t, "orders-v1")
	for _, d := range damages(t) {
		for _, sideName := range []string{"old", "new"} {
			bad := copyDir(t, good)
			d.apply(t, bad)
			for _, failOn := range []string{"", "never", "warn"} {
				args := []string{"--old", good, "--new", bad}
				if sideName == "old" {
					args = []string{"--old", bad, "--new", good}
				}
				if failOn != "" {
					args = append(args, "--fail-on", failOn)
				}
				for _, format := range []string{"text", "json"} {
					r := runTool(t, append(args, "--format", format)...)
					if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "can't be trusted") {
						t.Errorf("%s on %s side, --fail-on %q, %s: exit %d, stdout %q, stderr %q", d.name, sideName, failOn, format, r.code, r.stdout, r.stderr)
					}
				}
			}
		}
		// --keys reads one side.
		bad := copyDir(t, good)
		d.apply(t, bad)
		if r := runTool(t, "--keys", "--new", bad); r.code != 2 || r.stdout != "" {
			t.Errorf("--keys on %s: exit %d, stdout %q", d.name, r.code, r.stdout)
		}
	}
}

// A file cut between two declarations still compiles. It must not look clean.
func TestCutBetweenDeclarationsIsReported(t *testing.T) {
	good := example(t, "orders-v1")
	bad := copyDir(t, good)
	p := filepath.Join(bad, "api", "order.go")
	src, _ := os.ReadFile(p)
	i := bytes.Index(src, []byte("// Customer is"))
	if i < 0 {
		t.Fatal("fixture changed")
	}
	os.WriteFile(p, src[:i], 0o644)
	r := runTool(t, "--old", good, "--new", bad, "--format", "json")
	if r.code != 1 {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := changes(jsonReport(t, r)); len(got) != 1 || got[0] != "break api.Customer type-removed" {
		t.Errorf("findings: %q", got)
	}
}

func TestUnknownTypeFlag(t *testing.T) {
	r := runTool(t, "--old", example(t, "orders-v1"), "--new", example(t, "orders-v2"), "--type", "Ordr")
	if r.code != 2 || r.stdout != "" {
		t.Errorf("exit %d stdout %q", r.code, r.stdout)
	}
}

func TestUnrelatedModules(t *testing.T) {
	other := writeModule(t, map[string]string{"x/x.go": "package x\n\ntype Thing struct {\n\tA int `json:\"a\"`\n}\n"})
	for _, args := range [][]string{{"--old", other, "--new", example(t, "orders-v1")}, {"--old", example(t, "orders-v1"), "--new", other}} {
		r := runTool(t, args...)
		if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "same module") {
			t.Errorf("%q: exit %d\n%s%s", args, r.code, r.stdout, r.stderr)
		}
	}
}

// fakeGo puts a go command first on PATH that prints out and exits with code.
func fakeGo(t *testing.T, out string, code int) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake go command is a shell script")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "out")
	os.WriteFile(data, []byte(out), 0o644)
	script := "#!/bin/sh\ncat '" + data + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func TestBrokenGoListOutput(t *testing.T) {
	good := example(t, "orders-v1")
	valid := `{"ImportPath":"example.com/orders/api","Name":"api","Export":"/nonexistent/export.a","Module":{"Path":"example.com/orders","Main":true}}`
	cases := []struct {
		name, out string
		code      int
	}{
		{"nothing", "", 0},
		{"garbage", "\x00\x01not json at all", 0},
		{"cut off", valid[:40], 0},
		{"failed", "", 1},
		{"build log", "# example.com/orders/api\napi/order.go:3:2: undefined: x\n", 1},
		{"missing export file", valid, 0},
		{"no export data", `{"ImportPath":"example.com/orders/api","Name":"api","Module":{"Path":"example.com/orders","Main":true}}`, 0},
		{"only deps", `{"ImportPath":"fmt","Export":"/x","Standard":true,"DepOnly":true}`, 0},
	}
	for _, c := range cases {
		env := fakeGo(t, c.out, c.code)
		for _, failOn := range []string{"break", "never"} {
			r := runIn(t, "", env, "--old", good, "--new", good, "--fail-on", failOn)
			if r.code != 2 || r.stdout != "" {
				t.Errorf("%s, --fail-on %s: exit %d stdout %q stderr %q", c.name, failOn, r.code, r.stdout, r.stderr)
			}
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestBaseGitRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	mod := filepath.Join(repo, "services", "orders")
	os.MkdirAll(filepath.Dir(mod), 0o755)
	if err := os.Rename(copyDir(t, example(t, "orders-v1")), mod); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "v1")
	git(t, repo, "tag", "v1")
	v2, _ := os.ReadFile(filepath.Join(example(t, "orders-v2"), "api", "order.go"))
	os.WriteFile(filepath.Join(mod, "api", "order.go"), v2, 0o644)

	r := runIn(t, mod, nil, "--base", "v1", "--format", "json")
	if r.code != 1 {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if rep := jsonReport(t, r); rep.Breaks != 5 {
		t.Errorf("breaks = %d, want 5", rep.Breaks)
	}
	// The work tree is compared, so after committing, HEAD against itself is quiet.
	git(t, repo, "commit", "-q", "-am", "v2")
	if r := runIn(t, mod, nil, "--base", "HEAD", "--fail-on", "warn"); r.code != 0 {
		t.Errorf("HEAD against the committed tree: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, ref := range []string{"nope", "", "--output=/tmp/x", "v1:services"} {
		if r := runIn(t, mod, nil, "--base", ref); r.code != 2 || r.stdout != "" {
			t.Errorf("--base %q: exit %d stdout %q", ref, r.code, r.stdout)
		}
	}
	// A commit that doesn't contain the module.
	git(t, repo, "checkout", "-q", "--orphan", "empty")
	git(t, repo, "rm", "-rq", "--cached", ".")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "empty")
	git(t, repo, "checkout", "-q", "-f", "main")
	if r := runIn(t, mod, nil, "--base", "empty"); r.code != 2 || r.stdout != "" {
		t.Errorf("--base at a commit without the module: exit %d stdout %q", r.code, r.stdout)
	}
	notRepo := copyDir(t, example(t, "orders-v1"))
	if r := runIn(t, notRepo, []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(notRepo)}, "--base", "HEAD"); r.code != 2 || r.stdout != "" {
		t.Errorf("--base outside git: exit %d stdout %q", r.code, r.stdout)
	}
}
