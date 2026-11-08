// Command jsonbreak compares the JSON wire format of a Go module's structs
// between two revisions and reports changes that break existing JSON readers
// or writers: removed or renamed keys, changed value types, and new
// omitempty, nullable or custom encodings.
//
// It loads real type information with `go list -export` for each side, so it
// sees exactly what encoding/json sees, including types from dependencies.
// It never runs the code it checks.
//
// Exit codes: 0 no finding at or above --fail-on, 1 findings, 2 bad usage or
// an input that can't be trusted (a side that doesn't build, has no JSON
// types, or isn't the same module). Exit 2 ignores --fail-on.
package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

const version = "0.1.0"

// Severity levels, ordered.
const (
	sevInfo  = "info"
	sevWarn  = "warn"
	sevBreak = "break"
)

func sevRank(s string) int {
	switch s {
	case sevBreak:
		return 3
	case sevWarn:
		return 2
	case sevInfo:
		return 1
	}
	return 0
}

// untrusted marks an error that means the comparison can't be trusted.
type untrusted struct{ msg string }

func (u *untrusted) Error() string { return u.msg }

func distrust(format string, a ...any) error { return &untrusted{fmt.Sprintf(format, a...)} }

type options struct {
	oldDir, newDir string
	base           string
	tags           string
	pkgs           []string
	all            bool
	format         string
	failOn         string
	includeTypes   []string
	keys           bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jsonbreak", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	var pkgs, types_ stringList
	fs.StringVar(&o.oldDir, "old", "", "module directory of the old revision")
	fs.StringVar(&o.newDir, "new", "", "module directory of the new revision (default: current directory)")
	fs.StringVar(&o.base, "base", "", "git ref for the old revision; the module is read from that commit with git archive")
	fs.StringVar(&o.tags, "tags", "", "build tags passed to go list, comma separated")
	fs.Var(&pkgs, "pkg", "package pattern to check, relative to the module (repeatable, default ./...)")
	fs.Var(&types_, "type", "also compare this exported type, as pkgpath.Name or Name (repeatable)")
	fs.BoolVar(&o.all, "all", false, "compare every exported struct type, not only those with json tags")
	fs.StringVar(&o.format, "format", "text", "output format: text or json")
	fs.StringVar(&o.failOn, "fail-on", "break", "lowest severity that exits 1: break, warn or never")
	fs.BoolVar(&o.keys, "keys", false, "print the JSON keys of each type in --new (or the current directory) and exit")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: jsonbreak (--old DIR | --base GITREF) [--new DIR] [--pkg PATTERN]... [--type NAME]... [--all]\n")
		fmt.Fprintf(stderr, "       jsonbreak --keys [--new DIR] [--pkg PATTERN]... [--type NAME]... [--all]\n")
		fmt.Fprintf(stderr, "                 [--tags T,...] [--format text|json] [--fail-on break|warn|never]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "jsonbreak", version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "jsonbreak: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	o.pkgs, o.includeTypes = pkgs, types_
	if o.format != "text" && o.format != "json" {
		fmt.Fprintf(stderr, "jsonbreak: --format must be text or json, not %q\n", o.format)
		return 2
	}
	if o.failOn != sevBreak && o.failOn != sevWarn && o.failOn != "never" {
		fmt.Fprintf(stderr, "jsonbreak: --fail-on must be break, warn or never, not %q\n", o.failOn)
		return 2
	}
	if o.keys {
		if o.oldDir != "" || o.base != "" {
			fmt.Fprintln(stderr, "jsonbreak: --keys reads one side; use --new DIR, not --old or --base")
			return 2
		}
		if o.newDir == "" {
			o.newDir = "."
		}
		if err := listKeys(o, stdout); err != nil {
			fmt.Fprintf(stderr, "jsonbreak: %v\n", err)
			fmt.Fprintln(stderr, "jsonbreak: no result: the input can't be trusted")
			return 2
		}
		return 0
	}
	if (o.oldDir == "") == (o.base == "") {
		fmt.Fprintln(stderr, "jsonbreak: give exactly one of --old DIR or --base GITREF")
		return 2
	}
	if o.newDir == "" {
		o.newDir = "."
	}
	rep, err := compare(o)
	if err != nil {
		fmt.Fprintf(stderr, "jsonbreak: %v\n", err)
		var u *untrusted
		if errors.As(err, &u) {
			fmt.Fprintln(stderr, "jsonbreak: no result: the comparison can't be trusted")
		}
		return 2
	}
	if o.format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "jsonbreak: %v\n", err)
			return 2
		}
	} else {
		printText(stdout, rep)
	}
	if o.failOn == "never" {
		return 0
	}
	for _, f := range rep.Findings {
		if sevRank(f.Severity) >= sevRank(o.failOn) {
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Loading a side.

type side struct {
	label   string // as shown to the user
	dir     string // absolute module directory
	modPath string
	fset    *token.FileSet
	pkgs    map[string]*types.Package // by path relative to module root ("." for root)
}

type listPkg struct {
	Dir        string
	ImportPath string
	Name       string
	Export     string
	Standard   bool
	DepOnly    bool
	Incomplete bool
	Module     *struct {
		Path string
		Main bool
		Dir  string
	}
	Error *struct {
		Pos string
		Err string
	}
	DepsErrors []*struct {
		Pos string
		Err string
	}
}

var goCmd = "go"

func loadSide(label, dir string, o options) (*side, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, distrust("%s side: %v", label, err)
	}
	if !st.IsDir() {
		return nil, distrust("%s side: %s is not a directory", label, dir)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		return nil, distrust("%s side: no go.mod in %s; point it at the module root", label, dir)
	}
	patterns := o.pkgs
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	args := []string{"list", "-e", "-export", "-deps",
		"-json=Dir,ImportPath,Name,Export,Standard,DepOnly,Incomplete,Module,Error,DepsErrors"}
	if o.tags != "" {
		args = append(args, "-tags", o.tags)
	}
	args = append(args, "--")
	args = append(args, patterns...)
	cmd := exec.Command(goCmd, args...)
	cmd.Dir = abs
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	if runErr != nil {
		return nil, distrust("%s side doesn't build: go list failed: %v\n%s", label, runErr, firstLines(errb.String(), 12))
	}
	dec := json.NewDecoder(&out)
	exports := map[string]string{}
	var targets []listPkg
	var problems []string
	for {
		var p listPkg
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, distrust("%s side: go list printed output that isn't valid JSON: %v", label, err)
		}
		if p.ImportPath == "" {
			return nil, distrust("%s side: go list printed a package with no import path", label)
		}
		if p.Error != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", p.ImportPath, oneLine(p.Error.Err)))
		}
		for _, de := range p.DepsErrors {
			problems = append(problems, fmt.Sprintf("%s: %s", p.ImportPath, oneLine(de.Err)))
		}
		if p.ImportPath == "unsafe" {
			continue
		}
		if p.Export == "" && p.Error == nil {
			problems = append(problems, fmt.Sprintf("%s: go list gave no export data", p.ImportPath))
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly {
			targets = append(targets, p)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		problems = dedupe(problems)
		if len(problems) > 8 {
			problems = append(problems[:8], fmt.Sprintf("and %d more", len(problems)-8))
		}
		return nil, distrust("%s side doesn't build:\n  %s", label, strings.Join(problems, "\n  "))
	}
	if len(targets) == 0 {
		return nil, distrust("%s side: go list matched no packages in %s", label, dir)
	}
	s := &side{label: label, dir: abs, fset: token.NewFileSet(), pkgs: map[string]*types.Package{}}
	lookup := func(p string) (io.ReadCloser, error) {
		f, ok := exports[p]
		if !ok || f == "" {
			return nil, fmt.Errorf("no export data for %s", p)
		}
		return os.Open(f)
	}
	imp := importer.ForCompiler(s.fset, "gc", lookup)
	for _, p := range targets {
		if p.Module == nil || !p.Module.Main {
			continue // a pattern outside the main module
		}
		if s.modPath == "" {
			s.modPath = p.Module.Path
		} else if s.modPath != p.Module.Path {
			continue
		}
		if p.Name == "main" && strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		pkg, err := imp.Import(p.ImportPath)
		if err != nil {
			return nil, distrust("%s side: can't read export data for %s: %v", label, p.ImportPath, err)
		}
		s.pkgs[relPkg(s.modPath, p.ImportPath)] = pkg
	}
	if len(s.pkgs) == 0 {
		return nil, distrust("%s side: no packages of the main module matched", label)
	}
	return s, nil
}

func relPkg(mod, imp string) string {
	if imp == mod {
		return "."
	}
	return strings.TrimPrefix(imp, mod+"/")
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return "  " + strings.Join(lines, "\n  ")
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " ..."
	}
	return s
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// git: extract the old revision of the module into a temporary directory.

func extractGitRev(newDir, ref string) (string, func(), error) {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = newDir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	prefix, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return "", nil, distrust("--base needs --new to be inside a git work tree: %v", err)
	}
	prefix = strings.TrimSpace(prefix)
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, distrust("--base: %v", err)
	}
	top = strings.TrimSpace(top)
	commit, err := git("rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", nil, distrust("--base %q is not a commit in this repository", ref)
	}
	commit = strings.TrimSpace(commit)
	tmp, err := os.MkdirTemp("", "jsonbreak-base-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	cmd := exec.Command("git", "archive", "--format=tar", commit)
	cmd.Dir = top
	var errb bytes.Buffer
	cmd.Stderr = &errb
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, err
	}
	xerr := untar(pipe, tmp)
	werr := cmd.Wait()
	if xerr != nil || werr != nil {
		cleanup()
		return "", nil, distrust("git archive %s failed: %v %v %s", ref, xerr, werr, strings.TrimSpace(errb.String()))
	}
	return filepath.Join(tmp, filepath.FromSlash(prefix)), cleanup, nil
}

func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path in archive: %s", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks and other entries are skipped: they could point outside dst.
		}
	}
}

// ---------------------------------------------------------------------------
// JSON shape of Go types, following encoding/json's rules.

type jfield struct {
	key       string
	goPath    string // Go field path, such as "Base.ID"
	origin    string // named struct type that declares the field, or "" for anonymous
	typ       types.Type
	omitempty bool
	omitzero  bool
	quoted    bool // ,string option in effect
	tagged    bool
	pos       token.Pos
	badTag    string // a tag name encoding/json rejects
}

const textMarshaler = "MarshalText"

func hasMethod(t types.Type, name string) bool {
	if types.NewMethodSet(t).Lookup(nil, name) != nil {
		return true
	}
	if _, isPtr := t.(*types.Pointer); isPtr {
		return false
	}
	return types.NewMethodSet(types.NewPointer(t)).Lookup(nil, name) != nil
}

// customMethods names the custom JSON or text methods a type has, such as
// "MarshalJSON" or "MarshalJSON and UnmarshalJSON", or "" for none.
func customMethods(t types.Type, marshal, unmarshal string) string {
	m, u := hasMethod(t, marshal), hasMethod(t, unmarshal)
	switch {
	case m && u:
		return marshal + " and " + unmarshal
	case m:
		return marshal
	case u:
		return unmarshal
	}
	return ""
}

func typeName(t types.Type) string {
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		obj := n.Obj()
		if obj.Pkg() == nil {
			return obj.Name()
		}
		return obj.Pkg().Name() + "." + obj.Name()
	}
	return t.String()
}

func fullName(n *types.Named) string {
	if n.Obj().Pkg() == nil {
		return n.Obj().Name()
	}
	return n.Obj().Pkg().Path() + "." + n.Obj().Name()
}

// shape describes how a type is encoded.
type shape struct {
	kind     string // string, number, bool, array, object, map, any, custom, unsupported
	detail   string // extra words for string or custom kinds
	nullable bool
	elem     types.Type // array and map element
	key      string     // map key kind
	st       *types.Struct
	named    *types.Named // for object kinds of a named struct
}

func (s shape) describe() string {
	switch s.kind {
	case "array":
		return "array of " + shapeOf(s.elem).describe()
	case "map":
		return "object with " + s.key + " keys of " + shapeOf(s.elem).describe()
	case "object":
		if s.named != nil {
			return "object " + typeName(s.named)
		}
		return "object"
	}
	if s.detail != "" {
		return s.kind + " (" + s.detail + ")"
	}
	return s.kind
}

func shapeOf(t types.Type) shape {
	t = types.Unalias(t)
	nullable := false
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
		nullable = true
	}
	if n, ok := t.(*types.Named); ok {
		switch fullName(n) {
		case "time.Time":
			return shape{kind: "string", detail: "RFC 3339 time", nullable: nullable}
		case "encoding/json.Number":
			return shape{kind: "number", detail: "json.Number", nullable: nullable}
		case "encoding/json.RawMessage", "encoding/json/jsontext.Value":
			return shape{kind: "any", detail: "json.RawMessage", nullable: true}
		case "math/big.Int":
			return shape{kind: "number", detail: "big.Int", nullable: nullable}
		}
		if _, isIface := n.Underlying().(*types.Interface); !isIface {
			if m := customMethods(n, "MarshalJSON", "UnmarshalJSON"); m != "" {
				return shape{kind: "custom", detail: m + " on " + typeName(n), nullable: nullable}
			}
			if m := customMethods(n, "MarshalText", "UnmarshalText"); m != "" {
				return shape{kind: "string", detail: m + " on " + typeName(n), nullable: nullable}
			}
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		info := u.Info()
		switch {
		case info&types.IsBoolean != 0:
			return shape{kind: "bool", nullable: nullable}
		case info&types.IsString != 0:
			return shape{kind: "string", nullable: nullable}
		case info&types.IsComplex != 0:
			return shape{kind: "unsupported", detail: "complex numbers can't be encoded"}
		case info&(types.IsInteger|types.IsFloat) != 0:
			return shape{kind: "number", nullable: nullable}
		}
		return shape{kind: "unsupported", detail: u.String()}
	case *types.Slice:
		if b, ok := types.Unalias(u.Elem()).Underlying().(*types.Basic); ok && b.Kind() == types.Byte &&
			customMethods(u.Elem(), "MarshalJSON", "UnmarshalJSON") == "" && customMethods(u.Elem(), textMarshaler, "UnmarshalText") == "" {
			return shape{kind: "string", detail: "base64", nullable: true}
		}
		return shape{kind: "array", elem: u.Elem(), nullable: true}
	case *types.Array:
		return shape{kind: "array", elem: u.Elem(), nullable: nullable}
	case *types.Map:
		return shape{kind: "map", elem: u.Elem(), key: mapKeyKind(u.Key()), nullable: true}
	case *types.Interface:
		return shape{kind: "any", nullable: true}
	case *types.Struct:
		s := shape{kind: "object", st: u, nullable: nullable}
		if n, ok := t.(*types.Named); ok {
			s.named = n
		}
		return s
	}
	return shape{kind: "unsupported", detail: t.String()}
}

func mapKeyKind(k types.Type) string {
	k = types.Unalias(k)
	if b, ok := k.Underlying().(*types.Basic); ok && b.Info()&types.IsString != 0 {
		return "string"
	}
	if m := customMethods(k, textMarshaler, "UnmarshalText"); m != "" {
		return "text (" + m + " on " + typeName(k) + ")"
	}
	if b, ok := k.Underlying().(*types.Basic); ok && b.Info()&types.IsInteger != 0 {
		return "integer"
	}
	return "unsupported"
}

// parseTag returns the json name and options of a struct tag.
func parseTag(tag string) (name string, opts []string, present bool) {
	v, ok := reflect.StructTag(tag).Lookup("json")
	if !ok {
		return "", nil, false
	}
	parts := strings.Split(v, ",")
	return parts[0], parts[1:], true
}

func hasOpt(opts []string, o string) bool {
	for _, x := range opts {
		if x == o {
			return true
		}
	}
	return false
}

// isValidTag mirrors encoding/json.
func isValidTag(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

// structFields lists the JSON keys of a struct type the way encoding/json's
// typeFields does: embedded structs are flattened, shallower fields win, and
// among equally deep fields a single tagged one wins or all are dropped.
// skipped returns the Go names of direct fields tagged json:"-".
// embedded returns the full names of the named structs embedded at any depth.
func structFields(st *types.Struct, named *types.Named) (fields []jfield, skipped map[string]bool, embedded map[string]*types.Struct) {
	type entry struct {
		st     *types.Struct
		named  *types.Named
		goPath string
		depth  int
		index  []int
	}
	type cand struct {
		jfield
		depth int
		index []int
	}
	skipped = map[string]bool{}
	embedded = map[string]*types.Struct{}
	var all []cand
	current := []entry{}
	next := []entry{{st: st, named: named}}
	visited := map[*types.Struct]bool{}
	for len(next) > 0 {
		current, next = next, nil
		count := map[*types.Struct]int{}
		for _, e := range current {
			count[e.st]++
		}
		for _, e := range current {
			if visited[e.st] {
				continue
			}
			visited[e.st] = true
			if count[e.st] > 1 {
				// Embedded twice at one depth: encoding/json drops its fields as ambiguous.
				continue
			}
			origin := ""
			if e.named != nil {
				origin = fullName(e.named)
				if e.depth > 0 {
					embedded[origin] = e.st
				}
			}
			for i := 0; i < e.st.NumFields(); i++ {
				f := e.st.Field(i)
				name, opts, _ := parseTag(e.st.Tag(i))
				ft := types.Unalias(f.Type())
				if f.Embedded() {
					t := ft
					if p, ok := t.(*types.Pointer); ok {
						t = types.Unalias(p.Elem())
					}
					_, isStruct := t.Underlying().(*types.Struct)
					if !f.Exported() && !isStruct {
						continue
					}
				} else if !f.Exported() {
					continue
				}
				if name == "-" && len(opts) == 0 {
					if e.depth == 0 {
						skipped[f.Name()] = true
					}
					continue
				}
				badTag := ""
				if !isValidTag(name) {
					badTag = name
					name = ""
				}
				goPath := f.Name()
				if e.goPath != "" {
					goPath = e.goPath + "." + f.Name()
				}
				if f.Embedded() && name == "" {
					t := ft
					if p, ok := t.(*types.Pointer); ok {
						t = types.Unalias(p.Elem())
					}
					if est, ok := t.Underlying().(*types.Struct); ok {
						n, _ := t.(*types.Named)
						next = append(next, entry{st: est, named: n, goPath: goPath, depth: e.depth + 1, index: appendIndex(e.index, i)})
						continue
					}
				}
				key := name
				if key == "" {
					key = f.Name()
				}
				quoted := false
				if hasOpt(opts, "string") {
					t := ft
					if p, ok := t.(*types.Pointer); ok {
						t = types.Unalias(p.Elem())
					}
					if b, ok := t.Underlying().(*types.Basic); ok &&
						b.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) != 0 {
						quoted = true
					}
				}
				all = append(all, cand{jfield{
					key: key, goPath: goPath, origin: origin, typ: f.Type(),
					omitempty: hasOpt(opts, "omitempty"), omitzero: hasOpt(opts, "omitzero"),
					quoted: quoted, tagged: name != "", pos: f.Pos(), badTag: badTag,
				}, e.depth, appendIndex(e.index, i)})
			}
		}
	}
	// encoding/json orders fields by their index sequence.
	sort.SliceStable(all, func(i, j int) bool { return lessIndex(all[i].index, all[j].index) })
	// Dominance.
	byKey := map[string][]cand{}
	var order []string
	for _, c := range all {
		if _, ok := byKey[c.key]; !ok {
			order = append(order, c.key)
		}
		byKey[c.key] = append(byKey[c.key], c)
	}
	for _, k := range order {
		cs := byKey[k]
		minDepth := cs[0].depth
		for _, c := range cs {
			if c.depth < minDepth {
				minDepth = c.depth
			}
		}
		var top []cand
		for _, c := range cs {
			if c.depth == minDepth {
				top = append(top, c)
			}
		}
		if len(top) == 1 {
			fields = append(fields, top[0].jfield)
			continue
		}
		var tagged []cand
		for _, c := range top {
			if c.tagged {
				tagged = append(tagged, c)
			}
		}
		if len(tagged) == 1 {
			fields = append(fields, tagged[0].jfield)
		}
	}
	return fields, skipped, embedded
}

func appendIndex(idx []int, i int) []int {
	out := make([]int, len(idx)+1)
	copy(out, idx)
	out[len(idx)] = i
	return out
}

func lessIndex(a, b []int) bool {
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k] != b[k] {
			return a[k] < b[k]
		}
	}
	return len(a) < len(b)
}

// structHasJSONTag reports whether a struct or a struct it embeds has a json tag.
func structHasJSONTag(st *types.Struct) bool {
	return hasTagDeep(st, map[*types.Struct]bool{})
}

func hasTagDeep(st *types.Struct, seen map[*types.Struct]bool) bool {
	if seen[st] {
		return false
	}
	seen[st] = true
	for i := 0; i < st.NumFields(); i++ {
		if _, _, ok := parseTag(st.Tag(i)); ok {
			return true
		}
		f := st.Field(i)
		if !f.Embedded() {
			continue
		}
		t := types.Unalias(f.Type())
		if p, ok := t.(*types.Pointer); ok {
			t = types.Unalias(p.Elem())
		}
		if est, ok := t.Underlying().(*types.Struct); ok && hasTagDeep(est, seen) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Comparing.

type Finding struct {
	Severity string   `json:"severity"`
	Type     string   `json:"type"`
	Key      string   `json:"key,omitempty"`
	Change   string   `json:"change"`
	Message  string   `json:"message"`
	Pos      string   `json:"pos,omitempty"`
	SeenIn   []string `json:"seen_in,omitempty"`
}

type Report struct {
	Tool     string    `json:"tool"`
	Version  string    `json:"version"`
	Old      string    `json:"old"`
	New      string    `json:"new"`
	Module   string    `json:"module"`
	TypesOld int       `json:"json_types_old"`
	TypesNew int       `json:"json_types_new"`
	Compared int       `json:"types_compared"`
	Keys     int       `json:"keys_compared"`
	Opaque   []string  `json:"custom_encoders_not_checked,omitempty"`
	Findings []Finding `json:"findings"`
	Breaks   int       `json:"breaks"`
	Warnings int       `json:"warnings"`
	Infos    int       `json:"infos"`
}

type comparer struct {
	old, new *side
	rep      *Report
	done     map[[2]*types.Struct]bool
	seen     map[string]int // finding dedupe key -> index
	opaque   map[string]bool
	keys     int
}

func (c *comparer) posOf(s *side, p token.Pos) string {
	if !p.IsValid() {
		return ""
	}
	pp := s.fset.Position(p)
	if pp.Filename == "" {
		return ""
	}
	name := pp.Filename
	if rel, err := filepath.Rel(s.dir, name); err == nil && !strings.HasPrefix(rel, "..") {
		name = filepath.ToSlash(rel)
	}
	if pp.Line > 0 {
		return fmt.Sprintf("%s:%d", name, pp.Line)
	}
	return name
}

func (c *comparer) add(f Finding, seenIn string) {
	k := f.Severity + "\x00" + f.Type + "\x00" + f.Key + "\x00" + f.Message
	if i, ok := c.seen[k]; ok {
		if seenIn != "" && seenIn != c.rep.Findings[i].Type && !contains(c.rep.Findings[i].SeenIn, seenIn) {
			c.rep.Findings[i].SeenIn = append(c.rep.Findings[i].SeenIn, seenIn)
		}
		return
	}
	if seenIn != "" && seenIn != f.Type {
		f.SeenIn = []string{seenIn}
	}
	c.seen[k] = len(c.rep.Findings)
	c.rep.Findings = append(c.rep.Findings, f)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// rootType is an exported named struct type picked for comparison.
type rootType struct {
	id    string // relpkg.Name
	named *types.Named
	st    *types.Struct
}

func roots(s *side, o options) map[string]rootType {
	out := map[string]rootType{}
	for rel, pkg := range s.pkgs {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !tn.Exported() || tn.IsAlias() {
				continue
			}
			n, ok := tn.Type().(*types.Named)
			if !ok || n.TypeParams().Len() > 0 {
				continue
			}
			st, ok := n.Underlying().(*types.Struct)
			if !ok {
				continue
			}
			id := rel + "." + name
			want := o.all || structHasJSONTag(st) || typeWanted(o.includeTypes, rel, pkg.Path(), name)
			if want {
				out[id] = rootType{id: id, named: n, st: st}
			}
		}
	}
	return out
}

func typeWanted(list []string, rel, full, name string) bool {
	for _, t := range list {
		if t == name || t == rel+"."+name || t == full+"."+name {
			return true
		}
	}
	return false
}

func displayID(id string) string {
	if strings.HasPrefix(id, "./") {
		return id[2:]
	}
	if strings.HasPrefix(id, "..") {
		return id[2:]
	}
	return id
}

func compare(o options) (*Report, error) {
	oldDir := o.oldDir
	oldLabel := o.oldDir
	if o.base != "" {
		dir, cleanup, err := extractGitRev(o.newDir, o.base)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		oldDir, oldLabel = dir, o.base
	}
	oldS, err := loadSide("old", oldDir, o)
	if err != nil {
		return nil, err
	}
	oldS.label = oldLabel
	newS, err := loadSide("new", o.newDir, o)
	if err != nil {
		return nil, err
	}
	newS.label = o.newDir
	return compareSides(oldS, newS, o)
}

func compareSides(oldS, newS *side, o options) (*Report, error) {
	rep := &Report{Tool: "jsonbreak", Version: version, Old: oldS.label, New: newS.label, Module: newS.modPath, Findings: []Finding{}}
	oldR, newR := roots(oldS, o), roots(newS, o)
	rep.TypesOld, rep.TypesNew = len(oldR), len(newR)
	hint := "no exported struct has a json tag; pass --all or --type to pick types"
	if len(oldR) == 0 {
		return nil, distrust("old side (%s) has no JSON types to compare: %s", oldS.label, hint)
	}
	if len(newR) == 0 {
		return nil, distrust("new side (%s) has no JSON types to compare, but the old side has %d: %s", newS.label, len(oldR), hint)
	}
	for _, t := range o.includeTypes {
		if !anyMatch(oldR, t) && !anyMatch(newR, t) {
			return nil, distrust("--type %s matched no exported struct type on either side", t)
		}
	}
	common := 0
	for id := range oldR {
		if _, ok := newR[id]; ok {
			common++
		}
	}
	if common == 0 {
		return nil, distrust("no JSON type exists on both sides (old module %s, new module %s); are these revisions of the same module?", oldS.modPath, newS.modPath)
	}
	c := &comparer{old: oldS, new: newS, rep: rep, done: map[[2]*types.Struct]bool{}, seen: map[string]int{}, opaque: map[string]bool{}}
	if oldS.modPath != newS.modPath {
		c.add(Finding{Severity: sevInfo, Type: "module", Change: "module-path",
			Message: fmt.Sprintf("module path changed from %s to %s; types were matched by their path inside the module", oldS.modPath, newS.modPath)}, "")
	}
	ids := make([]string, 0, len(oldR))
	for id := range oldR {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		or := oldR[id]
		nr, ok := newR[id]
		if !ok {
			// A type that kept its name in a moved package is still the same wire type.
			c.add(Finding{Severity: sevBreak, Type: displayID(id), Change: "type-removed",
				Message: "type was removed or renamed, or is no longer a struct with json tags; readers of its keys can't be checked",
				Pos:     c.posOf(oldS, or.named.Obj().Pos())}, "")
			continue
		}
		rep.Compared++
		osh, nsh := shapeOf(or.named), shapeOf(nr.named)
		if osh.kind == "custom" || nsh.kind == "custom" {
			// The type encodes itself, so its fields aren't the wire format.
			c.compareTypes(displayID(id), "", or.named, nr.named, c.posOf(newS, nr.named.Obj().Pos()), "")
			continue
		}
		c.compareStruct(displayID(id), "", or.st, nr.st, displayID(id))
	}
	newIDs := make([]string, 0)
	for id := range newR {
		if _, ok := oldR[id]; !ok {
			newIDs = append(newIDs, id)
		}
	}
	sort.Strings(newIDs)
	for _, id := range newIDs {
		c.add(Finding{Severity: sevInfo, Type: displayID(id), Change: "type-added", Message: "new JSON type",
			Pos: c.posOf(newS, newR[id].named.Obj().Pos())}, "")
	}
	rep.Keys = c.keys
	for k := range c.opaque {
		rep.Opaque = append(rep.Opaque, k)
	}
	sort.Strings(rep.Opaque)
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) > sevRank(b.Severity)
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Key < b.Key
	})
	for _, f := range rep.Findings {
		switch f.Severity {
		case sevBreak:
			rep.Breaks++
		case sevWarn:
			rep.Warnings++
		default:
			rep.Infos++
		}
	}
	return rep, nil
}

func anyMatch(r map[string]rootType, t string) bool {
	for id, rt := range r {
		name := rt.named.Obj().Name()
		if t == name || t == id || t == rt.named.Obj().Pkg().Path()+"."+name {
			return true
		}
	}
	return false
}

// ownerOf names the type a finding belongs to: the named struct that declares
// the field, so a change in an embedded or shared type is reported once.
func ownerOf(f jfield, fallback string, s *side) string {
	if f.origin == "" {
		return fallback
	}
	full := f.origin
	if s.modPath != "" && strings.HasPrefix(full, s.modPath+".") {
		return strings.TrimPrefix(full, s.modPath+".") // root package
	}
	if s.modPath != "" && strings.HasPrefix(full, s.modPath+"/") {
		return strings.TrimPrefix(full, s.modPath+"/")
	}
	return full
}

// compareStruct compares two struct types. where is the type name the keys
// belong to, prefix the key path inside it (for anonymous nested structs).
func (c *comparer) compareStruct(where, prefix string, os_, ns *types.Struct, seenIn string) {
	pair := [2]*types.Struct{os_, ns}
	if c.done[pair] && prefix == "" {
		return
	}
	c.done[pair] = true
	of, _, oemb := structFields(os_, nil)
	nf, nskip, nemb := structFields(ns, nil)
	rel := func(sd *side, full string) string {
		if full == "" {
			return ""
		}
		return ownerOf(jfield{origin: full}, "", sd)
	}
	oldEmb, newEmb := map[string]*types.Struct{}, map[string]*types.Struct{}
	for k, v := range oemb {
		oldEmb[rel(c.old, k)] = v
	}
	for k, v := range nemb {
		newEmb[rel(c.new, k)] = v
	}
	// declares reports whether an embedded struct still has a key of its own.
	declares := func(st *types.Struct, key string) bool {
		fs, _, _ := structFields(st, nil)
		for _, f := range fs {
			if f.key == key {
				return true
			}
		}
		return false
	}
	// A key belongs to the embedded type that declares it only while both
	// sides still embed that type; otherwise the outer type changed.
	ownerBoth := func(o, n jfield) string {
		oo, no := rel(c.old, o.origin), rel(c.new, n.origin)
		if oo != "" && oo == no {
			return oo
		}
		return where
	}
	ownerGone := func(o jfield) string {
		// The embedded type itself lost the key: report it there, once.
		if oo := rel(c.old, o.origin); oo != "" && newEmb[oo] != nil && !declares(newEmb[oo], o.key) {
			return oo
		}
		return where
	}
	for _, f := range nf {
		if f.badTag == "" {
			continue
		}
		owner := where
		if no := rel(c.new, f.origin); no != "" {
			owner = no
		}
		c.add(Finding{Severity: sevWarn, Type: owner, Key: prefix + f.key, Change: "invalid-tag",
			Message: fmt.Sprintf("json tag name %q isn't valid for encoding/json: Go's original encoder uses the field name %q, and Go 1.27's encoder (built on encoding/json/v2) drops the field", f.badTag, f.key),
			Pos:     c.posOf(c.new, f.pos)}, seenIn)
	}
	oldBy := map[string]jfield{}
	newBy := map[string]jfield{}
	newByGo := map[string]jfield{}
	newFold := map[string]jfield{}
	for _, f := range of {
		oldBy[f.key] = f
	}
	for _, f := range nf {
		newBy[f.key] = f
		newByGo[f.goPath] = f
		newFold[strings.ToLower(f.key)] = f
	}
	oldGo := map[string]bool{}
	for _, f := range of {
		oldGo[f.goPath] = true
	}
	renamedTo := map[string]bool{}
	for _, f := range of {
		key := prefix + f.key
		if nfld, ok := newBy[f.key]; ok {
			c.keys++
			c.compareField(ownerBoth(f, nfld), key, f, nfld, seenIn)
			continue
		}
		owner := ownerGone(f)
		// Gone. Renamed, case-only renamed, hidden with "-", or removed?
		if nfld, ok := newByGo[f.goPath]; ok && !hasKey(oldBy, nfld.key) {
			renamedTo[nfld.key] = true
			msg := fmt.Sprintf("key %q renamed to %q (field %s)", f.key, nfld.key, fieldName(f))
			if strings.EqualFold(f.key, nfld.key) {
				msg += "; Go's encoding/json still decodes it case-insensitively, other JSON readers don't"
			}
			c.add(Finding{Severity: sevBreak, Type: owner, Key: key, Change: "key-renamed", Message: msg,
				Pos: c.posOf(c.new, nfld.pos)}, seenIn)
			c.keys++
			c.compareField(owner, prefix+nfld.key, f, nfld, seenIn)
			continue
		}
		if nfld, ok := newFold[strings.ToLower(f.key)]; ok && !hasKey(oldBy, nfld.key) {
			renamedTo[nfld.key] = true
			c.add(Finding{Severity: sevBreak, Type: owner, Key: key, Change: "key-renamed",
				Message: fmt.Sprintf("key %q is now %q (field %s); Go's encoding/json still decodes it case-insensitively, other JSON readers don't", f.key, nfld.key, fieldName(nfld)),
				Pos:     c.posOf(c.new, nfld.pos)}, seenIn)
			c.keys++
			c.compareField(owner, prefix+nfld.key, f, nfld, seenIn)
			continue
		}
		msg := fmt.Sprintf("key %q removed (field %s)", f.key, fieldName(f))
		if prefix == "" && !strings.Contains(f.goPath, ".") && nskip[f.goPath] {
			msg = fmt.Sprintf("key %q removed: field %s is now tagged json:\"-\"", f.key, f.goPath)
		} else if strings.Contains(f.goPath, ".") && owner == where {
			if oo := rel(c.old, f.origin); oo != "" && newEmb[oo] != nil {
				msg = fmt.Sprintf("key %q removed: field %s, promoted from embedded %s, is now hidden by another field with the same key", f.key, fieldName(f), oo)
			} else if oo != "" {
				msg = fmt.Sprintf("key %q removed (field %s, promoted from embedded %s)", f.key, fieldName(f), oo)
			} else {
				msg = fmt.Sprintf("key %q removed (field %s, promoted from an embedded struct)", f.key, f.goPath)
			}
		}
		c.add(Finding{Severity: sevBreak, Type: owner, Key: key, Change: "key-removed", Message: msg,
			Pos: c.posOf(c.old, f.pos)}, seenIn)
	}
	for _, f := range nf {
		if _, ok := oldBy[f.key]; ok || renamedTo[f.key] {
			continue
		}
		owner := where
		if no := rel(c.new, f.origin); no != "" && oldEmb[no] != nil && !declares(oldEmb[no], f.key) {
			owner = no
		}
		c.add(Finding{Severity: sevInfo, Type: owner, Key: prefix + f.key, Change: "key-added",
			Message: fmt.Sprintf("new key %q (field %s, %s)", f.key, fieldName(f), shapeOf(f.typ).describe()),
			Pos:     c.posOf(c.new, f.pos)}, seenIn)
	}
}

// fieldName is the Go field path inside the type that declares the field.
func fieldName(f jfield) string {
	if f.origin != "" {
		if i := strings.LastIndexByte(f.goPath, '.'); i >= 0 {
			return f.goPath[i+1:]
		}
	}
	return f.goPath
}

func hasKey(m map[string]jfield, k string) bool { _, ok := m[k]; return ok }

func (c *comparer) compareField(owner, key string, of, nf jfield, seenIn string) {
	pos := c.posOf(c.new, nf.pos)
	add := func(sev, change, msg string) {
		c.add(Finding{Severity: sev, Type: owner, Key: key, Change: change, Message: msg, Pos: pos}, seenIn)
	}
	if !of.omitempty && nf.omitempty {
		add(sevWarn, "omitempty-added", "now tagged omitempty: the key is left out when the value is empty, and readers that expect it will miss it")
	}
	if !of.omitzero && nf.omitzero {
		add(sevWarn, "omitzero-added", "now tagged omitzero: the key is left out when the value is zero (Go 1.24+), and readers that expect it will miss it")
	}
	if of.quoted != nf.quoted {
		if nf.quoted {
			add(sevBreak, "string-option", fmt.Sprintf("now tagged ,string: the value is encoded inside a JSON string, so %s becomes a quoted string", shapeOf(of.typ).describe()))
		} else {
			add(sevBreak, "string-option", fmt.Sprintf(",string option removed: the value was a quoted string and is now a plain %s", shapeOf(nf.typ).describe()))
		}
		return
	}
	c.compareTypes(owner, key, of.typ, nf.typ, pos, seenIn)
}

func (c *comparer) compareTypes(owner, key string, ot, nt types.Type, pos, seenIn string) {
	add := func(sev, change, msg string) {
		c.add(Finding{Severity: sev, Type: owner, Key: key, Change: change, Message: msg, Pos: pos}, seenIn)
	}
	os_, ns := shapeOf(ot), shapeOf(nt)
	if os_.kind == "custom" || ns.kind == "custom" {
		switch {
		case os_.kind == "custom" && ns.kind == "custom":
			if os_.detail != ns.detail {
				add(sevWarn, "custom-encoding", fmt.Sprintf("custom encoder changed from %s to %s; the shape can't be checked", os_.detail, ns.detail))
			} else {
				c.opaque[ns.detail] = true
			}
		case ns.kind == "custom":
			add(sevWarn, "custom-encoding", fmt.Sprintf("was %s, now encoded by %s; the new shape can't be checked", os_.describe(), ns.detail))
		default:
			add(sevWarn, "custom-encoding", fmt.Sprintf("was encoded by %s, now %s; the old shape can't be checked", os_.detail, ns.describe()))
		}
		return
	}
	if os_.kind != ns.kind && (os_.kind == "any" || ns.kind == "any") {
		if os_.kind == "any" {
			add(sevWarn, "type-narrowed", fmt.Sprintf("was any JSON value (%s), now %s: other values no longer decode", os_.describe(), ns.describe()))
		} else {
			add(sevWarn, "type-widened", fmt.Sprintf("was %s, now any JSON value (%s): readers can get other shapes", os_.describe(), ns.describe()))
		}
		return
	}
	if os_.kind != ns.kind {
		add(sevBreak, "type-changed", fmt.Sprintf("value changed from %s to %s", os_.describe(), ns.describe()))
		return
	}
	if !os_.nullable && ns.nullable && os_.kind != "any" {
		add(sevWarn, "now-nullable", fmt.Sprintf("can now be null (%s became %s)", types.TypeString(ot, qual), types.TypeString(nt, qual)))
	}
	switch os_.kind {
	case "string", "number":
		switch {
		case os_.detail == ns.detail:
		case os_.detail == "base64" || ns.detail == "base64":
			add(sevBreak, "type-changed", fmt.Sprintf("value changed from %s to %s", os_.describe(), ns.describe()))
		default:
			add(sevWarn, "encoding-changed", fmt.Sprintf("still a %s, but encoded differently: %s became %s", os_.kind, os_.describe(), ns.describe()))
		}
	case "array":
		c.compareTypes(owner, key+"[]", os_.elem, ns.elem, pos, seenIn)
	case "map":
		if os_.key != ns.key {
			add(sevBreak, "map-key-changed", fmt.Sprintf("object keys changed from %s to %s", os_.key, ns.key))
		}
		c.compareTypes(owner, key+"{}", os_.elem, ns.elem, pos, seenIn)
	case "object":
		if os_.named != nil && ns.named != nil {
			// Both named: compare them as types of their own, so a shared
			// type's change is reported once, under its name.
			where := c.typeID(ns.named, c.new)
			if oid := c.typeID(os_.named, c.old); oid != where {
				where = oid + " (now " + where + ")"
			}
			c.compareStruct(where, "", os_.st, ns.st, seenIn)
			return
		}
		c.compareStruct(owner, key+".", os_.st, ns.st, seenIn)
	}
}

func (c *comparer) typeID(n *types.Named, s *side) string {
	full := fullName(n)
	return ownerOf(jfield{origin: full}, full, s)
}

func qual(p *types.Package) string { return p.Name() }

// listKeys prints the JSON keys of every JSON type on one side.
func listKeys(o options, w io.Writer) error {
	s, err := loadSide("new", o.newDir, o)
	if err != nil {
		return err
	}
	r := roots(s, o)
	if len(r) == 0 {
		return distrust("%s has no JSON types: no exported struct has a json tag; pass --all or --type to pick types", o.newDir)
	}
	ids := make([]string, 0, len(r))
	for id := range r {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rt := r[id]
		if len(o.includeTypes) > 0 && !typeWanted(o.includeTypes, strings.SplitN(id, ".", 2)[0], rt.named.Obj().Pkg().Path(), rt.named.Obj().Name()) {
			continue
		}
		if sh := shapeOf(rt.named); sh.kind == "custom" {
			fmt.Fprintf(w, "%s: %s\n", displayID(id), sh.describe())
			continue
		}
		fields, _, _ := structFields(rt.st, nil)
		fmt.Fprintf(w, "%s:\n", displayID(id))
		for _, f := range fields {
			var flags []string
			if f.omitempty {
				flags = append(flags, "omitempty")
			}
			if f.omitzero {
				flags = append(flags, "omitzero")
			}
			desc := shapeOf(f.typ).describe()
			if f.quoted {
				desc = "string (quoted " + desc + ")"
			}
			if sh := shapeOf(f.typ); sh.nullable && sh.kind != "any" {
				desc += ", nullable"
			}
			line := fmt.Sprintf("  %s: %s", f.key, desc)
			if len(flags) > 0 {
				line += " [" + strings.Join(flags, ",") + "]"
			}
			fmt.Fprintln(w, line)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Output.

func printText(w io.Writer, r *Report) {
	fmt.Fprintf(w, "jsonbreak %s -> %s: %d JSON types compared, %d keys\n", r.Old, r.New, r.Compared, r.Keys)
	for _, f := range r.Findings {
		label := strings.ToUpper(f.Severity)
		target := f.Type
		if f.Key != "" {
			target += " " + f.Key
		}
		fmt.Fprintf(w, "  %-5s %s\n", label, target)
		fmt.Fprintf(w, "        %s\n", f.Message)
		if f.Pos != "" {
			fmt.Fprintf(w, "        at %s\n", f.Pos)
		}
		if len(f.SeenIn) > 0 {
			fmt.Fprintf(w, "        reached from %s\n", strings.Join(f.SeenIn, ", "))
		}
	}
	if len(r.Opaque) > 0 {
		fmt.Fprintf(w, "  not checked: values with custom JSON methods: %s\n", strings.Join(r.Opaque, ", "))
	}
	if r.Breaks == 0 && r.Warnings == 0 {
		fmt.Fprintf(w, "  no breaking changes or warnings in %d types", r.Compared)
		if r.Infos > 0 {
			fmt.Fprintf(w, " (%s)", plural(r.Infos, "addition"))
		}
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "  %d breaking, %s, %s\n", r.Breaks, plural(r.Warnings, "warning"), plural(r.Infos, "addition"))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
