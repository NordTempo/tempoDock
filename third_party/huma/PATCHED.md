# Huma v2.39.1, patched

A copy of [github.com/danielgtaylor/huma/v2](https://github.com/danielgtaylor/huma)
v2.39.1 (MIT, see LICENSE.md), wired in by the `replace` in the root
`go.mod`. Only the packages Dock builds or tests with are kept; tests and
docs are dropped.

The one change is in `huma.go`: `Register`'s body moved into the
non-generic `register`, which takes the input and output types as
`reflect.Type`. Upstream, Go stencils the whole body (about 25 KB of code
and metadata) once per input/output shape, which cost bin/dock 9.6 MB for
its ~330 operations. Now each operation adds a small wrapper.

To update Huma: copy the new release's non-test sources over these, keep
the packages listed by
`go list -deps -test ./... | grep danielgtaylor/huma`, and re-apply the
split (search for `func register(`). Drop this copy if upstream makes
`Register` non-generic.
