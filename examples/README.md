# Examples

- `orders-v1/` is a made-up order service API: `Order`, `Item`, `Customer` and an embedded `Meta`, with `time.Time`, a `money.Cents` type that has its own `MarshalJSON`, an anonymous nested struct and a map.
- `orders-v2/` breaks its wire format on purpose: a renamed key, a removed key, a field hidden with `json:"-"`, `,string` on a number, a nested number turned into a string, a new omitempty, a time that can now be null, and a float replaced by `money.Cents`. Run `make demo-break`, which exits 1.
- `orders-v1-refactor/` changes the Go code without changing the JSON: Go fields renamed and reordered, a field moved into an embedded struct, an anonymous struct replaced by a named one, integer and float sizes changed, and new keys added. Run `make demo-refactor`, which must stay quiet and exit 0 under `--fail-on warn`.

All three are separate modules, so the repo's own `go vet ./...` and `go test ./...` don't build them.
