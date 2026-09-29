module github.com/mendelbuild/mendel-integrations

go 1.26.1

// The wrapper protocol is Mendel's own module, which is private: fetching it
// needs GOPRIVATE=github.com/mendelbuild/mendelbuild and git access to the
// repository. Doc 35 §19 (2026-09-28) says the contract becomes a small
// public module of its own once the spike has settled it.

require github.com/mendelbuild/mendelbuild v0.0.0-20260929193812-0d8001ae0bf6
