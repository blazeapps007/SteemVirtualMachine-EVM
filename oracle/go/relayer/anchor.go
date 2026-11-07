package relayer

import "fmt"

// steemAnchor tracks this process's verification of the chain's Steem anchor:
// the Steem block a fresh chain starts from (relayer_start_block) and that
// block's ID (relayer_start_block_id), both recorded in genesis. Verified once
// per process (and again if governance ever moves the anchor).
type steemAnchor struct {
	verifiedID string
}

// verify checks this oracle's Steem node against the chain's anchor: the node
// must return the recorded ID for the anchor block. A different ID means the
// Steem RPC is on another network (or the genesis is wrong), and nothing it
// reports may be attested. An unrecorded ID (empty) verifies nothing.
func (a *steemAnchor) verify(steem *SteemClient, block uint64, id string) error {
	if id == "" || id == a.verifiedID {
		return nil
	}
	got, err := steem.BlockID(block)
	if err != nil {
		return fmt.Errorf("steem anchor: fetching block %d: %w", block, err)
	}
	if got != id {
		return fmt.Errorf("steem anchor mismatch: this chain starts at Steem block %d with ID %s, but the Steem RPC returns %s "+
			"(the RPC is on another network, or this node has the wrong genesis) — not attesting anything", block, id, got)
	}
	a.verifiedID = id
	return nil
}

// anchoredCursor never lets the scan cursor sit below the chain's Steem
// anchor: the chain rejects attestations of earlier blocks, which predate it
// (e.g. a previous chain's deposits to the same gateway). Scanning begins at
// cursor+1, so the lowest cursor is anchor-1.
func anchoredCursor(cursor, chainStart uint64) uint64 {
	if chainStart > 0 && cursor < chainStart-1 {
		return chainStart - 1
	}
	return cursor
}
