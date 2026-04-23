// Package dylib embeds the ObjC companion library so downstream users
// can `go install github.com/LocalKinAI/kinrec/cmd/kinrec` without
// building C code locally.
package dylib

import _ "embed"

//go:embed libkinrec_writer.dylib
var Bytes []byte
