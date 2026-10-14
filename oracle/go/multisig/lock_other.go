//go:build !unix

package multisig

import "os"

// lockStateDir is a no-op off Unix; the signer only runs in Linux containers.
func lockStateDir(dir string) (func(), error) {
	return func() {}, os.MkdirAll(dir, 0o700)
}
