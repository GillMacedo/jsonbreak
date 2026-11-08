# jsonbreak

Claude Code skill and Go CLI that compares the JSON wire format of a Go module's structs between two revisions and reports changes that break JSON readers or writers.

## Architecture
`plugin/scripts/jsonbreak.go` holds all the logic in one standard-library file, so `go run` needs no setup. `loadSide` runs `go list -e -export -deps -json` in each module directory and imports each package's export data with `go/importer`. `roots` picks exported structs with json tags. `structFields` lists a struct's keys with encoding/json's rules (tags, embedding, dominance). `shapeOf` describes how a type encodes. `compareStruct`, `compareField` and `compareTypes` produce findings. `extractGitRev` reads `--base` with `git archive`. `plugin/skills/checking-json-wire-compatibility/SKILL.md` tells Claude how to use it. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet, unit tests and the integration tests in `tests/`
- `make lint`: plugin and marketplace manifest validation
- `make demo`: the README examples on `examples/orders-v1`, `orders-v2` and `orders-v1-refactor`

## Conventions
- Standard library only. Never run the code being checked; `go list -export` and `git archive` are the only commands it runs.
- An input it can't fully trust (a side that doesn't build, `go list` output that isn't valid, missing export data, no JSON types, no type in common, a bad `--base` ref) exits 2 and prints nothing on stdout, whatever `--fail-on` says.
- A shape the tool can't see (custom JSON methods, a removed type) is a finding or a "not checked" line, never silence.
- Key rules must match encoding/json. `TestKeysMatchEncodingJSON` checks them against the real encoder of the Go running the tests; keep it passing on Go 1.22 and the latest Go.
- Tests must not assume the host GOOS or GOARCH.

## Do and don't
- Do keep README examples as real output from `make demo-break`, `make demo-refactor` and `make demo-keys`.
- Don't claim a change is safe for every reader. The tool reports what changes on the wire; whether a reader cares is the user's call.

## Definition of done
`make test`, `make lint` and `make demo` pass, and `ROADMAP.md`, `README.md` and docs are updated when relevant.
