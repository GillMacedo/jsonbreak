# Architecture

    parse flags
      --base REF: git rev-parse, git archive REF | untar to a temp dir (old side)
      --> for each side: go list -e -export -deps -json PATTERNS
            any Error, DepsErrors, missing export data, bad JSON or exit != 0 --> exit 2
          go/importer.ForCompiler("gc", lookup) reads each main-module package from its export data
      --> roots: exported, non-generic named structs with a json tag (directly or through embedding),
                 plus --type and --all; none on a side, or none in common --> exit 2
      --> for each root in both sides, matched by path inside the module + name:
            the type has custom JSON methods --> compareTypes (custom-encoding)
            else compareStruct:
              structFields(old), structFields(new) with encoding/json's rules
              same key        --> compareField: omitempty, omitzero, ,string, then compareTypes
              same Go field   --> key-renamed
              same key, other case --> key-renamed (case only)
              gone            --> key-removed (or "now tagged json:\"-\"")
              new             --> key-added
            compareTypes: shapeOf both sides; kind change, nullability, string details,
                          array and map elements, nested structs (named pairs compared once)
      --> roots only in old --> type-removed; only in new --> type-added
      --> text or JSON --> exit code

- **The compiler decides the types.** Both sides are compiled by `go list -export`, and the tool reads the compiler's export data. Type aliases, generics, types from dependencies and build tags are resolved the way `go build` resolves them, with no parser of its own.
- **Keys follow encoding/json.** `structFields` is a port of encoding/json's `typeFields`: tag name validity, `-` and `-,`, unexported fields, unexported embedded structs, embedded pointers, embedded structs with a tag name, breadth-first depth, and dominance (shallowest wins, then the only tagged field, else all are dropped). `tests/shapes_test.go` checks the key lists against `json.Marshal` of the Go running the tests.
- **Shapes are kinds, not Go types.** `int` to `float64` is still a JSON number and isn't reported. `time.Time` is a string, `json.Number` a number, `json.RawMessage` any value, `[]byte` a base64 string, and a type with `MarshalText` or `UnmarshalText` a string. A type with `MarshalJSON` or `UnmarshalJSON` is opaque: the tool reports when one appears, disappears or changes, and lists the rest as not checked.
- **A finding belongs to the type that declares the field.** A key promoted from an embedded struct is reported under that struct while both sides still embed it, so a change in a shared `Meta` type is reported once with the types it was reached from. When the outer struct stops embedding it, or another field hides the key, the outer struct gets the finding.
- **Types are matched by their path inside the module**, so a major version bump (`/v2`) still compares. A root type present only on the old side is a BREAK, because the tool can't tell a rename from a deletion, and a file cut off between two declarations looks the same.
- **Untrusted input stops the run.** Every failure to load a side exits 2 before any output is printed.

Key choices are in `decisions/`.
