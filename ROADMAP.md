# Roadmap

## Next
- A GitHub Action example that runs `jsonbreak --base` against the pull request's merge base and posts nothing without a person approving it
- Match renamed types by shape, so `Ruleset` renamed to `RepositoryRuleset` is compared instead of reported as removed
- An `--ignore` file for accepted breaks, with a reason per line

## Later
- Read `json:"...,inline"` and other encoding/json/v2 options once v2 is the default in a released Go
- YAML (`yaml` tags) and `mapstructure` tags, behind a flag
- Compare a struct with a JSON Schema or OpenAPI document instead of with another revision
