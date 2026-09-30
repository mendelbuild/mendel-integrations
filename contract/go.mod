// The contract between Mendel and a wrapper: the wire protocol
// (wrapperprotocol) and the conformance harness that checks a wrapper keeps
// it (conformance, cmd/conformance). Standard library only, and a module of
// its own, so that Mendel's server can check a wrapper it generated without
// depending on the module that holds the wrappers, and a wrapper author
// outside this repository can build against it with nothing else.
module github.com/mendelbuild/mendel-integrations/contract

go 1.26.1
