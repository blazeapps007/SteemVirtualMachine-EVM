//go:build unix

package multisig

import "testing"

// Two signers on one state dir would overwrite each other's votes (and could
// each approve a different proposal for one withdrawal): the second must fail.
func TestStateDirLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockStateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockStateDir(dir); err == nil {
		t.Fatal("a second signer took the lock")
	}
	unlock()
	again, err := lockStateDir(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	again()
}
