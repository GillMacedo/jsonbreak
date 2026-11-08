# 0003: A removed JSON type is a BREAK

**Decision:** a struct with json tags that exists in the old revision and not in the new one is reported as BREAK, not WARN.

**Why:** the tool can't tell whether the type was deleted, renamed or moved, and readers of its JSON may break in all three cases. A Go file cut off between two declarations still compiles and looks exactly like a deletion, so a softer severity would let a damaged checkout pass under the default `--fail-on break`.

**Alternatives:** matching renamed types by their key sets is on the roadmap. Until then, `--keys` on both sides shows whether a rename kept the shape.
