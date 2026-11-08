# 0002: Read compiler export data, not source

**Decision:** each side is loaded with `go list -e -export -deps -json` and `go/importer.ForCompiler(fset, "gc", lookup)`, which reads the export data files the compiler wrote.

**Why:** a JSON shape depends on types from other packages and modules (`time.Time`, a `uuid.UUID` with `MarshalText`, a `decimal.Decimal` with `MarshalJSON`), on aliases and on build tags. Export data has all of that resolved by the real compiler, including struct tags and method sets, with no type checker of our own. It also means a side that doesn't compile can't produce a result: `go list` reports the error and the tool exits 2.

**Costs:** both revisions must build, and their dependencies must be in the module cache or downloadable. Positions in findings come from export data and point at the field's line.
