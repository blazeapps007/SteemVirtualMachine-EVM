// steemAnchor.ts — the chain's Steem anchor (relayer_start_block +
// relayer_start_block_id, recorded in genesis): the Steem block a fresh chain
// starts from. Oracles verify their Steem node against it and never scan
// before it. Mirrors oracle/go/relayer/anchor.go.

export interface BlockIdSource {
  blockId(num: number): Promise<string>;
}

export class SteemAnchorMismatch extends Error {}

/** Tracks this process's verification of the anchor (verified once, and
 * again if governance ever moves it). */
export class SteemAnchor {
  verifiedId = "";

  /** This oracle's Steem node must return the recorded ID for the anchor
   * block, or nothing it reports may be attested. No recorded ID: nothing to
   * verify. */
  async verify(steem: BlockIdSource, block: number, id: string): Promise<void> {
    if (!id || id === this.verifiedId) return;
    const got = await steem.blockId(block);
    if (got !== id) {
      throw new SteemAnchorMismatch(
        `steem anchor mismatch: this chain starts at Steem block ${block} with ID ${id}, but the Steem RPC returns ${got} ` +
          "(the RPC is on another network, or this node has the wrong genesis) — not attesting anything",
      );
    }
    this.verifiedId = id;
  }
}

/** Never lets the scan cursor sit below the anchor: the chain rejects
 * attestations of earlier blocks (they predate it). Scanning begins at
 * cursor+1, so the lowest cursor is anchor-1. */
export function anchoredCursor(cursor: number, chainStart: number): number {
  return chainStart > 0 && cursor < chainStart - 1 ? chainStart - 1 : cursor;
}
