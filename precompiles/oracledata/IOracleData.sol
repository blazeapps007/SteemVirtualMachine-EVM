// SPDX-License-Identifier: LGPL-3.0-only
pragma solidity >=0.8.18;

/// @dev The IOracleData contract's address.
address constant ORACLEDATA_PRECOMPILE_ADDRESS = 0x0000000000000000000000000000000000000902;

/// @dev The IOracleData contract's instance.
IOracleData constant ORACLEDATA_CONTRACT = IOracleData(ORACLEDATA_PRECOMPILE_ADDRESS);

/**
 * @title IOracleData — SteemVM price feed precompile (x/oracle/data)
 * @dev Read-only access to the on-chain, validator-attested exchange rates.
 * Rates are the power-weighted median of the 2/3-consensus votes, refreshed once
 * per vote period (~1h), so a price may be up to ~1–2h stale.
 *
 * Rates are returned as fixed-point with 18 decimals (a rate of 1.5 is
 * 1500000000000000000), matching cosmossdk.io/math.LegacyDec's internal scale —
 * so an ERC20-style `rate / 1e18` recovers the human value. This is a mint-input
 * ONLY for EVM consumers / peg logic; the bridge mint stays 1:1 and never reads it.
 */
interface IOracleData {
    /**
     * @dev getPrice returns the latest finalized rate for a currency pair
     * (e.g. "STEEM/USD_External"). An unknown or not-yet-finalized pair returns all zeros
     * rather than reverting — probe with `rate != 0` (no real rate is 0).
     * @param pair The currency pair, e.g. "STEEM/USD_External".
     * @return rate The 18-decimal fixed-point rate.
     * @return updateEpoch The vote epoch (height / vote period) the rate was set.
     * @return updateTime The block time (unix seconds) the rate was set.
     */
    function getPrice(string memory pair)
        external
        view
        returns (uint256 rate, uint256 updateEpoch, uint256 updateTime);

    /**
     * @dev getPrices returns every finalized rate. The three arrays are parallel
     * (pairs[i] has rate rates[i] set at updateTimes[i]).
     * @return pairs The currency pairs.
     * @return rates The 18-decimal fixed-point rates.
     * @return updateTimes The block times (unix seconds) each rate was set.
     */
    function getPrices()
        external
        view
        returns (string[] memory pairs, uint256[] memory rates, uint256[] memory updateTimes);
}
