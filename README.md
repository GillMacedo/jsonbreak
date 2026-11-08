# jsonbreak: catch JSON breaking changes in Go structs before you merge

[![CI](https://github.com/GillMacedo/jsonbreak/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/jsonbreak/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI for Go backend developers and reviewers. It compares the JSON your Go structs encode to at two revisions and reports what breaks readers: renamed or removed json keys, a field hidden with `json:"-"`, a number that became a string, a new `,string` or `omitempty`, a value that can now be `null`, and new custom `MarshalJSON` methods.**

Changing `json:"customer_id"` to `json:"customerId"` compiles, passes `go vet`, passes your tests (they encode and decode with the same struct), and `apidiff` says nothing, because Go's API checkers look at Go identifiers, not struct tags. The break shows up in the mobile app, the other service, or the webhook consumer after the deploy. `jsonbreak` reads both revisions with the Go compiler, works out each struct's keys with `encoding/json`'s rules, and lists every change on the wire.

## What you can ask Claude

- "Does this PR break the JSON our Go API returns?"
- "I renamed a bunch of struct fields in our event package. Did any json keys change?"
- "We're about to tag v2.4.0 of our Go client. Is the wire format still compatible with v2.3.0?"
- "Which keys does `Order` encode to, and which are omitempty or nullable?"

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/jsonbreak
    claude plugin install jsonbreak@jsonbreak-marketplace

Or run it directly with Go 1.22 or newer. It has no dependencies:

    go run github.com/GillMacedo/jsonbreak/plugin/scripts@latest --base main

## Example

`examples/orders-v1` is a made-up order service API. `examples/orders-v2` is the next revision, with changes that all compile:

    $ make demo-break
    jsonbreak examples/orders-v1 -> examples/orders-v2: 4 JSON types compared, 19 keys
      BREAK api.Customer email
            key "email" removed: field Email is now tagged json:"-"
            at api/order.go:43
      BREAK api.Item sku
            key "sku" removed (field SKU)
            at api/order.go:35
      BREAK api.Order customer_id
            key "customer_id" renamed to "customerId" (field CustomerID)
            at api/order.go:19
      BREAK api.Order quantity
            now tagged ,string: the value is encoded inside a JSON string, so number becomes a quoted string
            at api/order.go:23
      BREAK api.Order shipping.days
            value changed from number to string
            at api/order.go:27
      WARN  api.Item price
            was number, now encoded by MarshalJSON on money.Cents; the new shape can't be checked
            at api/order.go:37
      WARN  api.Meta created_at
            can now be null (time.Time became *time.Time)
            at api/order.go:13
            reached from api.Customer, api.Order
      WARN  api.Order note
            now tagged omitempty: the key is left out when the value is empty, and readers that expect it will miss it
            at api/order.go:24
      INFO  api.Order currency
            new key "currency" (field Currency, string)
            at api/order.go:30
      not checked: values with custom JSON methods: MarshalJSON on money.Cents
      5 breaking, 3 warnings, 1 addition

Exit code 1. The `customer_id` rename is the one from the top of this page. Go field renames that keep the tag aren't reported. `shipping.days` is a key inside an anonymous struct. `created_at` is declared once in the embedded `Meta`, so it's reported once, with the types that embed it.

`examples/orders-v1-refactor` changes the Go code without changing the JSON: fields renamed and reordered, `customer_id` moved into an embedded struct, an anonymous struct replaced by a named one, and `int` changed to `int64`. It stays quiet and exits 0, even with `--fail-on warn`:

    $ make demo-refactor
    jsonbreak examples/orders-v1 -> examples/orders-v1-refactor: 4 JSON types compared, 21 keys
      INFO  api.Meta version
            new key "version" (field Version, number)
            at api/order.go:18
            reached from api.Customer, api.Order
      INFO  api.Order currency
            new key "currency" (field Currency, string)
            at api/order.go:43
      INFO  api.Ownership
            new JSON type
            at api/order.go:22
      INFO  api.Shipping
            new JSON type
            at api/order.go:27
      not checked: values with custom JSON methods: MarshalJSON on money.Cents
      no breaking changes or warnings in 4 types (4 additions)

To see what a type looks like on the wire:

    $ make demo-keys
    api.Order:
      id: string
      created_at: string (RFC 3339 time), nullable
      customerId: string
      status: string
      items: array of object api.Item, nullable
      total: custom (MarshalJSON on money.Cents)
      quantity: string (quoted number)
      note: string [omitempty]
      shipping: object
      tags: object with string keys of string, nullable
      currency: string

## Usage

    jsonbreak (--old DIR | --base GITREF) [--new DIR] [--pkg PATTERN]... [--type NAME]... [--all]
              [--tags T,...] [--format text|json] [--fail-on break|warn|never] [--strict-readers]
    jsonbreak --keys [--new DIR] [--pkg PATTERN]... [--type NAME]... [--all]

1. **Load both sides.** In each module directory it runs `go list -e -export -deps -json` (default pattern `./...`), which compiles the packages without linking or running them, and reads the compiler's type information. `--base REF` takes the old side from git with `git archive`, so the new side is your work tree, uncommitted changes included. `--new` defaults to the current directory.
2. **Pick JSON types.** Every exported, non-generic struct with a json tag, directly or through an embedded struct, plus the named structs their fields use. `--type pkg.Name` adds a struct without tags, and `--all` takes every exported struct. Types are matched by package path inside the module and name, so a `/v2` module path change still compares.
3. **Compare keys.** Keys come from `encoding/json`'s rules: tag names, `-` and `-,`, unexported fields, embedded structs and pointers, and which of two fields with the same key wins. Values are compared as JSON kinds, so `int` to `float64` is fine and `int` to `string` isn't.

- **`--format json`** prints the same report for scripts and agents, with `severity`, `type`, `key`, `change`, `message`, `pos` and `seen_in` per finding.
- **`--fail-on`** sets the lowest severity that exits 1: `break` by default, `warn`, or `never`.
- **`--strict-readers`** treats added JSON keys as `WARN` instead of `INFO`, for readers that call `Decoder.DisallowUnknownFields`.
- **`--keys`** prints the keys of each JSON type in one module, with omitempty, nullable and custom encoders marked.
- **`--tags`** passes build tags to `go list`. Other settings such as `GOFLAGS` and `GOWORK` come from the environment.

Exit 0 means no finding at or above `--fail-on`. Exit 1 means findings. Exit 2 means bad usage or a comparison it can't trust: a side that's missing, has no `go.mod`, doesn't compile (an empty, cut-off or garbage Go file or `go.mod`), a `go list` failure or output that isn't valid, missing export data, a side with no JSON types, two sides with no type in common, an unknown `--type`, or a `--base` ref that isn't a commit. Exit 2 ignores `--fail-on` and prints nothing on stdout.

## What it reports

| Finding | Severity | Meaning |
|---|---|---|
| `key-removed` | BREAK | The key is gone: field deleted, unexported, tagged `json:"-"`, its embedded struct removed, or hidden by another field with the same key |
| `key-renamed` | BREAK | The same Go field now has a different key. A case-only rename is flagged too: Go's decoder matches keys case-insensitively, other languages don't |
| `type-changed` | BREAK | The value's JSON kind changed: number, string, bool, array, object, base64 string. Checked inside arrays (`items[]`), maps (`tags{}`) and nested structs |
| `string-option` | BREAK | `,string` was added or removed, so a number or bool moves into or out of quotes |
| `map-key-changed` | BREAK | Map keys changed between string, integer and `MarshalText` keys |
| `type-removed` | BREAK | A JSON type exists only in the old revision. It may have been renamed; the tool can't tell |
| `omitempty-added`, `omitzero-added` | WARN | The key is now left out when empty or zero |
| `now-nullable` | WARN | `T` became `*T` (or an array became a slice), so readers can get `null` |
| `type-narrowed`, `type-widened` | WARN | `any` or `json.RawMessage` became a specific type, or the other way round |
| `custom-encoding` | WARN | A `MarshalJSON`, `UnmarshalJSON` or text method appeared, disappeared or changed type, so the shape can't be checked |
| `encoding-changed` | WARN | Still a string or number, but encoded another way, such as a plain string replaced by a `MarshalText` type |
| `invalid-tag` | WARN | A tag name `encoding/json` rejects. Go 1.22 uses the field name; Go 1.27.3's encoder dropped the field in our test |
| `key-added`, `type-added` | INFO | New keys and types. They never fail the run (unless `--strict-readers` turns `key-added` into WARN) |

Values with custom JSON methods on both sides are listed on a "not checked" line, because their shape lives in code.

## How it compares

- **`apidiff` and `gorelease`** (golang.org/x/exp, BSD-3-Clause) check Go API compatibility. Changing `json:"id"` to `json:"user_id"` and adding `,string` produced no output and exit 0 from `apidiff` in our test; deleting the field is reported, as a Go API change.
- **`relimpact`** (MIT) and **`gobreaker`** (Apache-2.0) wrap Go API diffs across git refs. Their READMEs don't mention struct tags or JSON keys.
- **`oasdiff`** and other OpenAPI diff tools compare specs. They help when you keep a spec up to date; `jsonbreak` reads the structs the service really encodes.
- **`go vet`'s `structtag` check** looks at the tags of one revision for syntax errors and duplicate keys. It doesn't compare revisions.

I searched GitHub and the web for a tool that compares Go struct JSON tags or JSON shapes between revisions and found none. Keyword search can miss projects.

## Limits

- **Both revisions must compile**, with their dependencies in the module cache or downloadable. If the old side needs a different Go version, `go` may try to download a toolchain.
- **`--base` reads the old side without your `go.work`.** It's extracted to a temporary directory, so a workspace that replaces dependencies with local copies only applies to the new side. If the old side can't resolve them, the run exits 2.
- **Custom JSON methods hide the shape.** A type with `MarshalJSON` or `UnmarshalJSON` is checked only for appearing, disappearing or changing to another encoder. Its fields aren't compared.
- **Renamed types aren't matched.** A JSON type renamed from `Ruleset` to `RepositoryRuleset` is a `type-removed` BREAK plus a `type-added` INFO. Fields that point to the renamed type are still compared, under `Ruleset (now RepositoryRuleset)`.
- **It reports wire changes and doesn't know who reads them.** A removed key nobody reads is still a BREAK. Use `--fail-on never` or `--pkg` to scope it.
- **encoding/json only.** Other encoders (`jsoniter`, `easyjson` generated code, `sonic`) usually follow the same tags, but their options aren't modeled. YAML, `mapstructure`, protobuf and `xml` tags aren't read.
- **encoding/json/v2 options** such as `inline`, `unknown` and `format:` aren't modeled.
- **Generic structs are skipped**, because their shape depends on the type arguments.
- Only json tags on struct fields are read. Values built with `map[string]any` at run time aren't seen.

## FAQ

**How do I check whether a Go struct change breaks JSON clients?** Run `jsonbreak --base main` in the module root, or let the skill pick the merge base. It lists every key that was removed, renamed or changed type.

**Does renaming a Go struct field change its JSON key?** Only if the field has no json tag, or the tag changes. `jsonbreak` reports the key change and stays quiet for Go-only renames.

**Is adding `omitempty` a breaking change?** It can be: readers that expect the key, or that treat a missing key differently from `0`, `""` or `false`, will see a difference. It's a WARN, so it fails the run only with `--fail-on warn`.

**Does changing `int` to `*int` break JSON?** Readers can now get `null` where they always got a number. It's a WARN.

**Why doesn't `apidiff` or `go vet` catch this?** They check Go identifiers and types, and struct tags aren't part of Go API compatibility. A tag change can't break code that compiles against your package, only code that reads its JSON.

**Does it run my code or tests?** No. `go list -export` compiles packages to export data, without linking or running anything. cgo packages run the C compiler, as `go build` does.

**Does it need network access?** No, as long as the dependencies of both revisions are in the module cache.

**Is it an MCP server?** No. It's a skill with one standard-library Go file, so `go run` is the whole install.

## Tested on

- Unit tests for tag parsing, tag name validity, severities, flag errors and the archive extractor, which refuses paths that leave its directory and archives that are cut off.
- Integration tests in `tests/` that build the tool and run it on the three examples, on each example against itself, on the tool's own module, and on 37 small before-and-after modules covering each finding and the close cases that must stay quiet (Go field renames, reordering, `int` to `float64`, embedded pointers, `,string` on a struct, generics).
- A ground-truth test that compiles a module with tricky field rules (embedded structs at several depths, two fields with one key, `-,`, tags with options only, unexported embedded structs, embedded non-struct types) and checks `--keys` against the keys `json.Marshal` really writes. It passes with Go 1.22.12, where encoding/json is the original encoder, and with Go 1.27.3, where it's built on encoding/json/v2.
- Damaged inputs, on the old side and the new side in turn with the other side real: an empty directory, an empty Go file, garbage, a Go file cut off partway, `go build` and `go test` failure output in place of a Go file, a type error, an empty, garbage, cut-off and build-log `go.mod`, a missing `go.mod`, and a module with no json tags. Each exits 2 with nothing on stdout, with the default `--fail-on`, `--fail-on never` and `--fail-on warn`, in text and JSON. A file cut off between two declarations still compiles; it's reported as a `type-removed` BREAK and exits 1. A fake `go` command printed nothing, garbage, cut-off JSON, a build log, a failure, JSON pointing to missing export data and JSON with only dependencies; all exit 2.
- `--base` in a real git repository with the module in a subdirectory: a tag against the changed work tree, HEAD against a committed tree, unknown refs, a ref that looks like a flag, a commit without the module, and a directory outside git.
- Go 1.22.12 and Go 1.27.3 on linux/arm64, with `-race`. The CI workflow runs the same tests on linux/amd64 with Go 1.22 and the latest stable Go.

Run on real releases of google/go-github, from module zips, on 2026-11-08:

- **v68.0.0 to v69.0.0**: 50 BREAK and 8 WARN findings. They include the release notes' `User.InheritedFrom` change from `*Team` to `[]*Team` (object to array), the ruleset refactor (46 removed or renamed types, and `configuration`, `rule_type` and `pattern` in ruleset change events going from `{"from": [...]}` to `{"from": "..."}`), and `RepositoryRuleset.id` becoming omitempty and nullable. The release notes' Go field typo fixes (`Rerequstable` to `Rerequestable`) kept their json tags and weren't reported, as intended.
- **v69.2.0 to v70.0.0**: the release notes' change of `PackageVersion.Body` and `Metadata` to `json.RawMessage` is reported as two WARN findings, and `Package.repository` was really removed in that release.
- **v69.0.0 to v69.2.0**, two minor releases: quiet, exit 0 under `--fail-on warn`, with three new types.

Not tested: Windows or macOS as the machine running the tool, modules that need cgo, very large monorepos, and use in a fresh Claude session.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/jsonbreak/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

### How pull requests work

1. **Open a PR.** Fork the repo, work on a branch, and keep it to one change. Write a title that says what changes for users, because it becomes the changelog line: `feat:` goes under Added and `fix:` under Fixed.
2. **Automatic check.** Code changes need tests, and `make test` must pass. Changes over 30 lines need a linked open issue (`Closes #<number>`), and a PR can have at most 800 changed lines. If something is missing, a comment tells you what to fix.
3. **Review.** Claude reviews each PR automatically. Code PRs also get a security review and are tested in a sandbox. The review then merges the PR or asks for changes, and a small problem may become a follow-up issue instead. A PR with no new commit 14 days after a change request is closed, and you can reopen it. PRs that change CI, agent settings, npm or Python dependencies, vendored code or build files can't be merged this way, and a comment says so. The same goes for anything the security review doesn't clear.
4. **Release.** A merge that changes what users install becomes a new version within about two hours, with a GitHub Release and, if the project has one, its npm package. Docs and CI changes wait for the next release.

## AI disclosure

This project was largely generated with Claude Code and reviewed by its maintainer.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `jsonbreak` saved you some prompts, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
