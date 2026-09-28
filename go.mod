module github.com/mendelbuild/mendel-integrations

go 1.26.1

// Mendel's module path (github.com/bhs/mendelbuild) is not where its
// repository lives (github.com/mendelbuild/mendelbuild), so the contract
// cannot be fetched by path yet. Until that is settled, the wrapper protocol
// is read from a checkout beside this one.
require github.com/bhs/mendelbuild v0.0.0

replace github.com/bhs/mendelbuild => ../mendelbuild
