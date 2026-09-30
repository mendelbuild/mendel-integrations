module github.com/mendelbuild/mendel-integrations

go 1.26.1

// The contract is a module of its own (contract/), and every wrapper here
// builds against the copy beside it, so a change to the contract and the
// wrappers it affects land in one commit.
require github.com/mendelbuild/mendel-integrations/contract v0.0.0

replace github.com/mendelbuild/mendel-integrations/contract => ./contract
