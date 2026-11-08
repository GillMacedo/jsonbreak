# Changelog

All notable changes are documented here, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-08

### Added
- Compares the JSON wire format of a Go module's exported structs between two revisions, from `--old DIR --new DIR` or `--base GITREF`, using type information from `go list -export`.
- Follows encoding/json's field rules: tag names, `-` and `-,`, invalid tag names, unexported fields, embedded and pointer-embedded structs, and dominance between fields with the same key.
- BREAK findings for removed and renamed keys (including case-only renames and fields tagged `json:"-"`), value type changes, `,string` added or removed, map key changes and removed types. WARN findings for new omitempty or omitzero, values that can now be null, `any` narrowed or widened, custom JSON or text methods added or changed, and tag names that Go releases encode differently. INFO findings for new keys and types.
- Special handling for `time.Time`, `json.Number`, `json.RawMessage`, `big.Int`, `[]byte` and `encoding.TextMarshaler`.
- `--keys` to print the JSON keys of each type, `--pkg`, `--type`, `--all`, `--tags`, `--format json`, `--fail-on`, and exit 2 for any input it can't trust.
- `checking-json-wire-compatibility` skill and plugin manifest.
