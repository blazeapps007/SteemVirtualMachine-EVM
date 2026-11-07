// Package msvectors embeds the shared svm.bank multisig conformance vectors
// (vectors.json in this directory) so the Go oracle can run them as a
// startup selftest. The Python and JS oracle clients load the same JSON file
// in their own test suites and selftests; it is the single source of truth
// that keeps the three byte-exact implementations in agreement.
package msvectors

import _ "embed"

// JSON is the raw contents of vectors.json.
//
//go:embed vectors.json
var JSON []byte
