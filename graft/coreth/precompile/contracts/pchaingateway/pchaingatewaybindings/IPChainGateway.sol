// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

// SPDX-License-Identifier: Ecosystem
pragma solidity ^0.8.0;

/// @title P-chain gateway (Flare stateful precompile at 0x0300000000000000000000000000000000000000)
/// @notice Read-only access to accepted P-chain blocks and primary-network staking
/// transactions during EVM execution. Every read is bounded by the P-chain height
/// committed in the C-chain block header (`getPChainHeight()`), so all nodes return
/// identical data.
interface IPChainGateway {
    // Block kinds returned by `getBlock`:
    //   0 Standard, 1 Proposal (decision txs first, proposal tx last; the outcome is the kind of the
    //   block at height+1), 2 Commit, 3 Abort, 4 Atomic (pre-Apricot-Phase-5 blocks only).
    //
    // Transaction types (bit `t` of a type filter selects type `t`; a zero filter selects all):
    //   1 AddPermissionlessValidator, 2 AddPermissionlessDelegator, 3 AddAutoRenewedValidator,
    //   4 SetAutoRenewedValidatorConfig, 5 RewardAutoRenewedValidator,
    //   6 AddValidator (pre-Durango legacy), 7 AddDelegator (pre-Durango legacy).
    //
    // Input orders accepted by `getTransactionInputs` and `getStakeOwner`:
    //   0 canonical (serialized order: UTXO tx id bytes, then output index),
    //   1 by the CB58 string of the UTXO tx id, then output index.

    struct Input {
        bytes32 utxoTxId;
        uint32 utxoOutputIndex;
        uint64 amount; // nFLR
        bool resolved; // false if the consumed output could not be located; owner fields are then zero
        uint64 locktime;
        uint32 threshold;
        bytes20[] addresses; // all owners of the consumed output, on-chain (byte) order; P-chain addresses
        uint32[] sigIndices; // positions in `addresses` whose signatures authorized the spend
    }

    struct Tx {
        uint8 txType;
        uint32 index; // position in the block's raw transaction list
        bytes32 txId;
        bytes20 nodeId;
        uint64 startTime; // block timestamp post-Durango, else the tx's own start field
        uint64 endTime; // stake end / first auto-renew cycle end / reward cycle end
        uint64 period; // auto-renew cycle length; for config txs the next cycle length (0 = exit)
        uint64 weight; // nFLR
        uint32 delegationShares; // delegation fee times 10,000
        uint32 autoCompoundRewardShares; // millionths
        bytes32 stakerTxId; // referenced AddAutoRenewedValidatorTx for config and reward txs
        uint32 inputCount;
        Input[] inputs; // first `maxInputsPerTx` inputs in canonical order
    }

    /// @notice P-chain height committed in the current block header; every read must be at or below it.
    function getPChainHeight() external view returns (uint64);

    /// @notice Accepted P-chain block at `height`. `timestamp` is 0 for pre-Banff blocks.
    function getBlock(
        uint64 height
    ) external view returns (bytes32 blockId, bytes32 parentId, uint64 timestamp, uint8 kind, uint32 txCount);

    /// @notice Decoded staking transactions of the block at `height`, scanning raw indexes from
    /// `fromIndex`, returning at most `maxTxs` transactions matching `typeFilter` with up to
    /// `maxInputsPerTx` inline inputs each. `nextIndex` is the raw index to continue from; it equals
    /// `txCount` when the block is exhausted.
    function getTransactions(
        uint64 height,
        uint32 fromIndex,
        uint32 maxTxs,
        uint256 typeFilter,
        uint32 maxInputsPerTx
    ) external view returns (Tx[] memory txs, uint32 nextIndex);

    /// @notice Inputs `[fromInput, fromInput+maxInputs)` of the transaction at `txIndex` in the given
    /// `order`, and the total number of inputs.
    function getTransactionInputs(
        uint64 height,
        uint32 txIndex,
        uint32 fromInput,
        uint32 maxInputs,
        uint8 order
    ) external view returns (Input[] memory inputs, uint32 inputCount);

    /// @notice Stake owner attribution compatible with the Flare P-chain indexer: the first input in
    /// the given `order`, restricted to the owners that signed it, choosing the lexically smallest
    /// bech32 address string. `resolved` is false when the input is missing, unresolved, a genesis UTXO or an
    /// ownerless output (threshold 0, no addresses), which resolves for `getTransactionInputs` but cannot be attributed.
    function getStakeOwner(
        uint64 height,
        uint32 txIndex,
        uint8 order
    ) external view returns (bytes20 owner, bool resolved);
}
