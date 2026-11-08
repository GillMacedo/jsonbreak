---
name: checking-json-wire-compatibility
description: Compares the JSON that a Go module's structs encode to at two revisions (a git ref or two directories) and reports wire-format breaks that compile cleanly, such as renamed or removed json keys, a json tag changed to "-", value types that changed (number to string, object to array), a new ,string option, new omitempty or omitzero, fields that can now be null, and custom MarshalJSON or UnmarshalJSON methods. Use when reviewing a pull request or release that touches Go API, event, webhook, config or message structs, when a client broke after a Go service deploy, or before tagging a Go client library.
---

# Checking Go JSON wire compatibility

The bundled tool needs Go 1.22 or newer and git for `--base`. It runs `go list -export` on each revision, which compiles packages without linking or running them, and reads the type information the compiler wrote. It never runs the project's code.

## 1. Pick the two revisions

- In a pull request, the old side is the merge base: `git merge-base origin/main HEAD`. Use the branch name only if the user asks for it.
- For a release, use the last tag (`git describe --tags --abbrev=0`) against the work tree.
- `--base REF` reads the old side with `git archive`, so uncommitted changes in the work tree are the new side. Use `--old DIR --new DIR` for two checkouts.
- Run it in the module root (the directory with `go.mod`). In a repo with several modules, run it once per module that has JSON types.

## 2. Run it

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/jsonbreak.go" --base "$(git merge-base origin/main HEAD)" --new MODULE_DIR

- By default it compares every exported struct that has a json tag, directly or through an embedded struct, plus the named structs their fields use. Add `--type pkg.Name` for structs without tags that are still encoded, or `--all` for every exported struct.
- Restrict packages with `--pkg ./api/...` when the module also has CLI or internal packages.
- Pass `--tags` if the JSON types sit behind build tags, and set `GOFLAGS` as CI does.
- Add `--format json` to parse the result.
- `--keys --new DIR --type Name` prints the keys of one type, which helps you explain a finding.

## Exit codes

0 means no BREAK finding (or no WARN with `--fail-on warn`). 1 means findings. 2 means the comparison can't be trusted: a side doesn't build, has no JSON types, has no type in common with the other side, or a `--base` ref doesn't exist. Treat 2 as unknown, never as a pass, and show the user the error.

## Report

1. List each BREAK with the type, key and file line. Say who it breaks: readers of the old key (other services, mobile apps, stored JSON, webhooks) and, for decoders, clients that still send the old shape.
2. For a renamed key, suggest keeping the old key readable during a migration, such as a second field or a custom `UnmarshalJSON`, only if the user wants a fix.
3. For WARN findings, explain the risk in one line each: omitempty means readers can miss the key, nullable means readers can get `null`, custom methods mean the tool couldn't see the shape.
4. Say what wasn't checked: the "not checked" line lists types with custom JSON methods, and a removed type may have been renamed. Check renamed types by hand with `--keys` on both sides.
5. INFO lines are additions. Mention them only if the user asks, or if a reader rejects unknown keys (`DisallowUnknownFields`).
6. Don't edit code unless the user asks.
