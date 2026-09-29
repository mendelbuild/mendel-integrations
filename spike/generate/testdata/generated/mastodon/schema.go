package main

import _ "embed"

// socialPostSchema is social_post.family.schema.json, embedded so the
// manifest's kind shape is always exactly the file this wrapper was tested
// against, never a hand-copied restatement of it.
//
//go:embed social_post.family.schema.json
var socialPostSchema []byte
