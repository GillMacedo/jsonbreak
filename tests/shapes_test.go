package tests

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// Each case compares a small old and new package and lists the expected
// findings as "severity key change", or nothing for a change that keeps the
// wire format.
func TestShapeChanges(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []string
	}{
		{"go field rename keeps key", "type T struct{ A int `json:\"a\"` }", "type T struct{ B int `json:\"a\"` }", nil},
		{"field order", "type T struct{ A int `json:\"a\"`; B string `json:\"b\"` }", "type T struct{ B string `json:\"b\"`; A int `json:\"a\"` }", nil},
		{"int to float is still a number", "type T struct{ A int `json:\"a\"` }", "type T struct{ A float64 `json:\"a\"` }", nil},
		{"named string type", "type T struct{ A string `json:\"a\"` }", "type S string\ntype T struct{ A S `json:\"a\"` }", nil},
		{"array to slice", "type T struct{ A [2]int `json:\"a\"` }", "type T struct{ A []int `json:\"a\"` }", []string{"warn a now-nullable"}},
		{"case-only rename", "type T struct{ A int `json:\"userId\"` }", "type T struct{ A int `json:\"userID\"` }", []string{"break userId key-renamed"}},
		{"tag removed falls back to field name", "type T struct{ UserID int `json:\"user_id\"`; X int `json:\"x\"` }", "type T struct{ UserID int; X int `json:\"x\"` }", []string{"break user_id key-renamed"}},
		{"key named dash", "type T struct{ A int `json:\"-,\"` }", "type T struct{ A int `json:\"-\"`; B int `json:\"b\"` }", []string{"break - key-removed", "info b key-added"}},
		{"omitzero added", "type T struct{ A int `json:\"a\"` }", "type T struct{ A int `json:\"a,omitzero\"` }", []string{"warn a omitzero-added"}},
		{"string option removed", "type T struct{ A int64 `json:\"a,string\"` }", "type T struct{ A int64 `json:\"a\"` }", []string{"break a string-option"}},
		{"string option on a struct is ignored", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A U `json:\"a\"` }", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A U `json:\"a,string\"` }", nil},
		{"bytes to strings", "type T struct{ A []byte `json:\"a\"` }", "type T struct{ A []string `json:\"a\"` }", []string{"break a type-changed"}},
		{"bytes to string", "type T struct{ A []byte `json:\"a\"` }", "type T struct{ A string `json:\"a\"` }", []string{"break a type-changed"}},
		{"map key type", "type T struct{ A map[string]int `json:\"a\"` }", "type T struct{ A map[int]int `json:\"a\"` }", []string{"break a map-key-changed"}},
		{"map value type", "type T struct{ A map[string]int `json:\"a\"` }", "type T struct{ A map[string]bool `json:\"a\"` }", []string{"break a{} type-changed"}},
		{"slice element type", "type T struct{ A []int `json:\"a\"` }", "type T struct{ A []string `json:\"a\"` }", []string{"break a[] type-changed"}},
		{"object to array", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A *U `json:\"a\"` }", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A []*U `json:\"a\"` }", []string{"break a type-changed"}},
		{"any narrowed", "type T struct{ A any `json:\"a\"` }", "type T struct{ A string `json:\"a\"` }", []string{"warn a type-narrowed"}},
		{"raw message to object", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A json.RawMessage `json:\"a\"` }", "type U struct{ X int `json:\"x\"` }\ntype T struct{ A U `json:\"a\"` }", []string{"warn a type-narrowed"}},
		{"unexported field ignored", "type T struct{ A int `json:\"a\"`; b int }", "type T struct{ A int `json:\"a\"`; c string }", nil},
		{"field made unexported", "type T struct{ A int `json:\"a\"`; B int `json:\"b\"` }", "type T struct{ A int `json:\"a\"`; b int }", []string{"break b key-removed"}},
		{"embedded struct removed", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base; A int `json:\"a\"` }", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ A int `json:\"a\"` }", []string{"break id key-removed"}},
		{"embedded struct given a tag nests it", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base; A int `json:\"a\"` }", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base `json:\"base\"`; A int `json:\"a\"` }", []string{"break id key-removed", "info base key-added"}},
		{"embedded pointer is the same", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base; A int `json:\"a\"` }", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ *Base; A int `json:\"a\"` }", nil},
		{"shallower field hides an embedded one", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base; A int `json:\"a\"` }", "type Base struct{ ID int `json:\"id\"` }\ntype T struct{ Base; A int `json:\"a\"`; ID string `json:\"id\"` }", []string{"break id type-changed"}},
		{"two embedded with one key cancel out", "type B1 struct{ ID int `json:\"id\"` }\ntype B2 struct{ X int `json:\"x\"` }\ntype T struct{ B1; B2 }", "type B1 struct{ ID int `json:\"id\"` }\ntype B2 struct{ X int `json:\"x\"`; ID int `json:\"id\"` }\ntype T struct{ B1; B2 }", []string{"break T id key-removed", "info B2 id key-added"}},
		{"type made custom", "type T struct{ A int `json:\"a\"` }", "type T struct{ A int `json:\"a\"` }\nfunc (T) MarshalJSON() ([]byte, error) { return nil, nil }", []string{"warn custom-encoding"}},
		{"field type made custom", "type U int\ntype T struct{ A U `json:\"a\"` }", "type U int\nfunc (*U) UnmarshalJSON([]byte) error { return nil }\ntype T struct{ A U `json:\"a\"` }", []string{"warn a custom-encoding"}},
		{"text marshaler is a string", "type T struct{ A string `json:\"a\"` }", "type U int\nfunc (U) MarshalText() ([]byte, error) { return nil, nil }\ntype T struct{ A U `json:\"a\"` }", []string{"warn a encoding-changed"}},
		{"time stays a string", "type T struct{ A time.Time `json:\"a\"` }", "type T struct{ A time.Time `json:\"a\"` }", nil},
		{"time to unix seconds", "type T struct{ A time.Time `json:\"a\"` }", "type T struct{ A int64 `json:\"a\"` }", []string{"break a type-changed"}},
		{"anonymous struct field", "type T struct{ A struct{ X int `json:\"x\"` } `json:\"a\"` }", "type T struct{ A struct{ Y int `json:\"y\"` } `json:\"a\"` }", []string{"break a.x key-removed", "info a.y key-added"}},
		{"recursive type", "type T struct{ Kids []T `json:\"kids\"`; N int `json:\"n\"` }", "type T struct{ Kids []T `json:\"kids\"`; N string `json:\"n\"` }", []string{"break n type-changed"}},
		{"generic types are skipped", "type G[X any] struct{ V X `json:\"v\"` }\ntype T struct{ A int `json:\"a\"` }", "type G[X any] struct{ W X `json:\"w\"` }\ntype T struct{ A int `json:\"a\"` }", nil},
		{"type removed", "type T struct{ A int `json:\"a\"` }\ntype U struct{ B int `json:\"b\"` }", "type T struct{ A int `json:\"a\"` }", []string{"break U type-removed"}},
		{"invalid tag name", "type T struct{ A int `json:\"a\"` }", "type T struct{ A int `json:\"a\"`; B int `json:\"b'c\"` }", []string{"info B key-added", "warn B invalid-tag"}},
		{"type added", "type T struct{ A int `json:\"a\"` }", "type T struct{ A int `json:\"a\"` }\ntype U struct{ B int `json:\"b\"` }", []string{"info U type-added"}},
	}
	header := "package p\n\nimport (\n\t\"encoding/json\"\n\t\"time\"\n)\n\nvar _ json.RawMessage\nvar _ time.Time\n\n"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := writeModule(t, map[string]string{"p/p.go": header + c.old + "\n"})
			nw := writeModule(t, map[string]string{"p/p.go": header + c.new + "\n"})
			r := runTool(t, "--old", old, "--new", nw, "--format", "json")
			if r.code == 2 {
				t.Fatalf("exit 2: %s", r.stderr)
			}
			var got []string
			for _, f := range jsonReport(t, r).Findings {
				typ := strings.TrimPrefix(f.Type, "p.")
				s := f.Severity + " " + strings.TrimSpace(f.Key+" "+f.Change)
				if typ != "T" {
					s = f.Severity + " " + strings.TrimSpace(typ+" "+f.Key+" "+f.Change)
				}
				if strings.Contains(c.name, "cancel") {
					s = f.Severity + " " + strings.TrimSpace(typ+" "+f.Key+" "+f.Change)
				}
				got = append(got, strings.Join(strings.Fields(s), " "))
			}
			sort.Strings(got)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("findings:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			wantCode := 0
			for _, w := range c.want {
				if strings.HasPrefix(w, "break") {
					wantCode = 1
				}
			}
			if r.code != wantCode {
				t.Errorf("exit %d, want %d", r.code, wantCode)
			}
		})
	}
}

// groundTruth is a module whose types exercise encoding/json's field rules.
// Its main program prints the keys encoding/json really writes for each type,
// with every field set, and the test checks them against jsonbreak --keys.
const groundTruthTypes = `package shapes

import "time"

type Base struct {
	ID      int    ` + "`json:\"id\"`" + `
	Created time.Time ` + "`json:\"created\"`" + `
	Hidden  string ` + "`json:\"hidden\"`" + `
}

type Other struct {
	Hidden string ` + "`json:\"hidden\"`" + `
	Extra  int
	Clash  int ` + "`json:\"clash\"`" + `
}

type inner struct {
	Promoted string ` + "`json:\"promoted\"`" + `
}

type Named int

type Wire struct {
	Base
	*Other
	inner
	Named
	Tagged   Base ` + "`json:\"tagged\"`" + `
	Clash    string
	Plain    string
	Renamed  string ` + "`json:\"renamed_key\"`" + `
	Skip     string ` + "`json:\"-\"`" + `
	Dash     string ` + "`json:\"-,\"`" + `
	Bad      string ` + "`json:\"bad,key\"`" + `
	OnlyOpts string ` + "`json:\",omitempty\"`" + `
	Quoted   int    ` + "`json:\"quoted,string\"`" + `
	Ptr      *int   ` + "`json:\"ptr\"`" + `
	List     []Base ` + "`json:\"list\"`" + `
	Map      map[string]int ` + "`json:\"map\"`" + `
	private  int
}

type Deep struct {
	Wire
	Level int ` + "`json:\"level\"`" + `
}
`

const groundTruthMain = `package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"example.com/m/shapes"
)

func fill(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.Ptr:
		if depth > 4 {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		if v.Type().PkgPath() == "time" {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fill(v.Field(i), depth+1)
			} else if v.Type().Field(i).Anonymous {
				// Unexported embedded struct: fill through unsafe-free reflection is not possible,
				// but its exported fields are promoted and settable via the outer value.
			}
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf(1))
		v.Set(m)
	}
}

func main() {
	for _, x := range []any{&shapes.Base{}, &shapes.Other{}, &shapes.Wire{}, &shapes.Deep{}} {
		v := reflect.ValueOf(x)
		fill(v.Elem(), 0)
		b, err := json.Marshal(x)
		if err != nil {
			panic(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("%s %s\n", v.Elem().Type().Name(), strings.Join(keys, ","))
	}
}
`

func TestKeysMatchEncodingJSON(t *testing.T) {
	dir := writeModule(t, map[string]string{"shapes/shapes.go": groundTruthTypes, "cmd/truth/main.go": groundTruthMain})
	cmd := exec.Command("go", "run", "./cmd/truth")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	truth := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, keys, _ := strings.Cut(line, " ")
		truth[name] = keys
	}
	r := runTool(t, "--keys", "--new", dir, "--pkg", "./shapes", "--all")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	got := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasPrefix(line, "shapes.") {
			cur = strings.TrimSuffix(strings.TrimPrefix(line, "shapes."), ":")
			got[cur] = []string{}
			continue
		}
		if strings.HasPrefix(line, "  ") && cur != "" {
			k, _, _ := strings.Cut(strings.TrimSpace(line), ": ")
			got[cur] = append(got[cur], k)
		}
	}
	if len(truth) != 4 {
		t.Fatalf("ground truth: %v", truth)
	}
	for name, keys := range truth {
		g := got[name]
		sort.Strings(g)
		if strings.Join(g, ",") != keys {
			t.Errorf("%s: jsonbreak keys %s\nencoding/json keys %s", name, strings.Join(g, ","), keys)
		}
	}
	// Sanity: the cases the fixture is meant to cover are really present.
	for _, k := range []string{"-", "renamed_key", "bad", "OnlyOpts", "Named", "promoted", "tagged"} {
		if !strings.Contains(","+truth["Wire"]+",", ","+k+",") {
			t.Errorf("ground truth for Wire lacks %q: %s", k, truth["Wire"])
		}
	}
	var _ = json.Valid
}
