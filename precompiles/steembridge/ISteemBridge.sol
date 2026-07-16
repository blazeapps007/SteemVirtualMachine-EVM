// SPDX-License-Identifier: Apache-2.0
pragma solidity >=0.8.18;

/// @dev The ISteemBridge contract's address.
address constant STEEMBRIDGE_PRECOMPILE_ADDRESS = 0x0000000000000000000000000000000000000900;

/// @dev The ISteemBridge contract's instance.
ISteemBridge constant STEEMBRIDGE_CONTRACT = ISteemBridge(STEEMBRIDGE_PRECOMPILE_ADDRESS);

/// @author SteemVM
/// @title SteemBridge Precompiled Contract
/// @notice EVM interface to the x/steembridge module: confirm Steem name
/// registrations, bridge asteem back to Steem, and resolve Steem account
/// names on-chain.
/// @custom:address 0x0000000000000000000000000000000000000900
interface ISteemBridge {
    /// @notice Emitted when a name registration is confirmed and the link
    /// becomes active.
    /// @param confirmer The address that confirmed (the memo-derived destination)
    /// @param registrationId The confirmed registration's id
    /// @param steemAccount The Steem account name now linked to the confirmer
    event NameConfirmed(
        address indexed confirmer,
        uint64 registrationId,
        string steemAccount
    );

    /// @notice Emitted when asteem is burned for withdrawal to Steem.
    /// @param sender The address whose asteem was burned
    /// @param destinationSteemAccount The Steem account to receive the STEEM
    /// @param amountAsteem The burned amount in asteem (18 decimals)
    /// @param memo The memo to attach to the Steem-side transfer
    /// @param withdrawalId The id of the recorded withdrawal, for tracking
    event BridgeOutRequested(
        address indexed sender,
        string destinationSteemAccount,
        uint256 amountAsteem,
        string memo,
        uint64 withdrawalId
    );

    /// @notice Confirm a name registration that has reached the validator
    /// attestation threshold. msg.sender is bound as the confirmer and must
    /// be the registration's memo-derived destination address, otherwise the
    /// call reverts. Note the equivalent Cosmos transaction (MsgConfirmName)
    /// is fee-exempt; this EVM path costs normal gas.
    /// @param registrationId The id of the registration to confirm
    /// @return success true on success
    function confirmName(uint64 registrationId) external returns (bool success);

    /// @notice Burn asteem from msg.sender and record a withdrawal for
    /// validators to relay to Steem. The amount must be a positive multiple
    /// of 10^15 asteem (one millisteem, STEEM's smallest unit). Sending to
    /// Steem account "null" is the provable-burn path.
    /// @param destinationSteemAccount The Steem account to receive the STEEM
    /// @param amountAsteem The amount to burn, in asteem (18 decimals)
    /// @param memo The memo to attach to the Steem-side transfer
    /// @return success true on success
    function bridgeOut(
        string calldata destinationSteemAccount,
        uint256 amountAsteem,
        string calldata memo
    ) external returns (bool success);

    /// @notice Resolve an active Steem account name link. Returns zero
    /// values (addr == address(0)) when the name has no active link — it
    /// does not revert, so contracts can probe cheaply.
    /// @param steemAccount The Steem account name to resolve
    /// @return addr The linked address (address(0) if unset)
    /// @return registrationId The registration that created the link
    /// @return linkedAt The block height the link was activated
    function resolveName(
        string calldata steemAccount
    ) external view returns (address addr, uint64 registrationId, uint64 linkedAt);

    /// @notice List the Steem account names actively linked to an address.
    /// @param owner The address to look up
    /// @return steemAccounts The linked Steem account names
    function namesOf(
        address owner
    ) external view returns (string[] memory steemAccounts);

    /// @notice List the registration ids currently awaiting confirmation by
    /// a destination address.
    /// @param destination The destination address
    /// @return registrationIds The confirmable registration ids
    function awaitingRegistrationIds(
        address destination
    ) external view returns (uint64[] memory registrationIds);
}
