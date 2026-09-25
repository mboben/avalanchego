# P-chain gateway precompile (Flare)

Address: `0x0300000000000000000000000000000000000000` · config key: `pChainGatewayConfig` · interface:
[`pchaingatewaybindings/IPChainGateway.sol`](pchaingatewaybindings/IPChainGateway.sol) · active from the Helicon
upgrade on the SAE C-chain (`vms/saevm/cchain`).

The gateway lets contracts **pull** accepted P-chain blocks and primary-network staking transactions during EVM
execution. The node never decides which P-chain data enters a C-chain block; a contract (typically one driven by the
Flare daemon) chooses what to read. It replaces the off-chain attestation feed of `PChainStakeMirror`.

## Determinism

Reading another chain during execution is only safe if every node reads the same thing, live, during bootstrapping
and in every historical replay (`eth_call`, `debug_trace*`). Three mechanisms guarantee that:

1. **Header-committed bound.** The SAE block builder writes the ProposerVM block's own P-chain height
   (`block.Context.ProposerPChainHeight`: the height the proposer sealed into the outer block, chosen via
   `GetMinimumHeight`, so at least ~30 s old, monotonic, and already accepted by every verifier's P-chain before the
   block is verified in normal operation) into `HeaderExtra.PChainHeight`. Verifiers rebuild the block with their own
   outer block's height and require a hash match, so the value is consensus-verified. The ACP-181 *epoch* height
   (`block.Context.PChainHeight`, pinned for 5 minutes) stays with warp predicate verification and is **not** the bound.
   `getPChainHeight()` returns the bound and **every read must be at or below it**; higher heights revert. It advances
   with every C-chain block, so a new P-chain transaction becomes readable roughly 30 s later, in the next C-chain block.
   The bound is also exposed as `pChainHeight` in the block JSON of `eth_getBlockByNumber/ByHash`.
2. **Accepted data only.** The reader (`vms/platformvm/pchainreader`, reachable through `snow.Context.PChainReader`)
   serves only accepted P-chain blocks, transactions and their owners; nothing depends on the local tip, mempool or
   wall clock.
3. **Execution barrier.** Before executing a block canonically the SAE executor waits until the local P-chain has
   accepted the committed height (`hook.ExecutionGate`, implemented by the cchain hooks). This only bites at the
   bootstrapping edge, where blocks are accepted without verification while the P-chain still catches up. RPC replays
   do not wait: on a lagging node they fail with an error instead.

## Error model

| Situation | Behaviour |
|---|---|
| Caller errors: height above the bound, no committed height (pre-Helicon block), page size above a cap, tx index / input index out of range, unsupported tx type, invalid order, malformed calldata | Solidity `Error(string)` revert with a `PChainGateway: …` reason; remaining gas is kept |
| Node-local failures: no P-chain reader on the chain context, committed height not accepted locally (RPC on a lagging node), database errors | `InvalidateExecution` + error: canonical execution halts loudly instead of diverging, RPC returns the error. Never a catchable revert, so a daemon cannot swallow it and advance its cursor. |

## Gas

Gas is charged **before** the corresponding work and depends only on the arguments (page sizes) and on consensus
data (the stored size of the P-chain block and of the producing transactions that are decoded, the number of owner
addresses returned). It never depends on node-local caches: a cached block or owner is charged exactly like a cold
one. There are no refunds for short pages. Constants in `gas.go`:

| Item | Gas |
|---|---|
| `getPChainHeight` | 200 |
| block read (every other call) | 20 000 |
| per requested transaction slot (`maxTxs`) | 5 000 |
| per requested input slot (`maxInputsPerTx` per tx slot, `maxInputs`, one for `getStakeOwner`) | 10 000 |
| per byte of P-chain data decoded: the block's stored bytes on every call, plus the stored bytes of each producing transaction resolved for an input (genesis UTXOs: none) | 10 |
| per input of the transaction when a non-empty page (`maxInputs > 0`, `fromInput < inputCount`) is read with `order = 1` (`getTransactionInputs`, `getStakeOwner`): ordering by CB58 string touches every input | 1 000 |
| per returned owner address (per candidate for `getStakeOwner`) | 200 |

Caps: `maxTxs ≤ 32`, `maxInputsPerTx ≤ 8`, `maxInputs ≤ 64`. A full `getTransactions(…, 32, …, 8)` page costs
2 740 000 gas plus bytes and addresses. Byte pricing follows the measured cold path: parsing plus decoding runs at
about 65 MB/s on an M-series laptop (`BenchmarkParse*` in `vms/platformvm/pchainreader`), about 1.5 gas/byte at
100 Mgas/s, so 10 gas/byte leaves a margin of more than 6× for slower validators. A typical staking block (a few KB)
costs tens of thousands of gas; a 128 KiB block (the P-chain builder's target) about 1.3M; the 2 MiB maximum about
21M. The node never parses under the P-chain lock: it reads the stored bytes under the lock (`state.GetBlockBytes`,
`state.GetTxBytes`), charges them, and parses outside. Reads that cannot pay for the bytes run out of gas before
any parsing happens. The local tip height (bound checks, the execution gate) comes from the accepted-height index,
never from parsing the tip block, so a P-chain tip change costs a few index lookups rather than an unmetered parse.
Input ordering is priced the same way: `order = 1` converts every input's UTXO transaction ID to its CB58 string and
sorts them whatever the page size (about 1.5 µs per input, `BenchmarkPermutationByString`), so a non-empty page in that
order pays 1 000 gas per input of the transaction, before anything is ordered and also when the reader serves the
cached order. Empty pages (`maxInputs = 0` or `fromInput = inputCount`) and canonical-order pages order nothing and pay
nothing for it; canonical pages are sliced directly from the decoded transaction. A call decodes and pays for its block once: `pchain.Reader.GetBlock` returns a handle
(`pchain.BlockReader`) over the decoded block and the per-transaction input reads of `getTransactions` go through
that handle, so they charge only the producing transactions they resolve.

## Data model

Type codes (bit `t` of `typeFilter` selects type `t`; `0` selects all): 1 `AddPermissionlessValidatorTx`,
2 `AddPermissionlessDelegatorTx`, 3 `AddAutoRenewedValidatorTx`, 4 `SetAutoRenewedValidatorConfigTx`,
5 `RewardAutoRenewedValidatorTx`, 6 `AddValidatorTx` (legacy), 7 `AddDelegatorTx` (legacy). Everything else, and
permissionless stakers of other subnets, is skipped but still counted in `txCount` and in the raw indexes.

| Type | startTime | endTime | period | weight | shares | stakerTxId |
|---|---|---|---|---|---|---|
| 1 / 2 / 6 / 7 | block timestamp if Durango is active at that block, else the tx's own start | tx end | 0 | tx weight | `DelegationShares` (validators) | 0 |
| 3 | block timestamp | start + `Period` | `Period` | sum of stake outputs | `DelegationShares`, `AutoCompoundRewardShares` | 0 |
| 4 | block timestamp | 0 | next `Period` (0 = exit at cycle end) | 0 | `AutoCompoundRewardShares` | add tx |
| 5 | block timestamp | cycle end | 0 | 0 | 0 | add tx |

Block kinds: 0 Standard, 1 Proposal, 2 Commit, 3 Abort, 4 Atomic (pre-Apricot-Phase-5). A Proposal block lists
its decision transactions first and the proposal transaction (type 5, or a legacy staking tx in Apricot-era blocks)
last; the proposal's outcome is the kind of the block at `height+1`, readable once `height+1 ≤ bound`. Auto-renewed
validators keep their original transaction id across cycles; contracts own the lifecycle interpretation.
Pre-Banff blocks report `timestamp = 0`.

### Inputs and owners

`getTransactionInputs` pages through the consumed UTXOs of one transaction. Each input carries the UTXO id, the amount,
the owner of the consumed output (`locktime`, `threshold`, all `addresses` in on-chain byte order, resolved from the
producing transaction: BaseTx outputs, returned stake, reward UTXOs and genesis UTXOs) and `sigIndices`, the owner
slots whose signatures authorized the spend. `resolved == false` means the producing output is unknown (owner fields
zero). Addresses are P-chain addresses (`bytes20`), not EVM addresses.

Orders: `0` canonical — the serialized order, which the P-chain requires to be by UTXO transaction id **bytes**, then
output index (the indexer's `in_idx`); `1` by the **CB58 string** of the UTXO transaction id, then output index. The
two differ only when the CB58 strings have different lengths.

### `getStakeOwner`

Reproduces the attribution rule of the Flare P-chain indexer (`utils/staking/utils.go` `DedupeTxs` and
`indexer/shared` `selectSigners`): take the first input in the requested order, keep only the owners that signed it
(all owners if there are no usable signature indices), and pick the lexically smallest **bech32** string with the
network's HRP. Genesis inputs (UTXOs of the genesis allocation, which have no producing transaction; their
`utxoTxId` is zero on production networks) and unresolved inputs yield `resolved == false`, even though
`getTransactionInputs` reports the genesis allocation's owner for them. So does a located output with threshold 0:
secp256k1fx permits an owner with no addresses (spendable without signatures), `getTransactionInputs` reports it as
`resolved` with an empty `addresses`, but there is nobody to attribute the stake to, so `getStakeOwner` returns
`(0, false)`. Output resolution and stake attribution are different conditions. Bech32 string order is not byte
order, which is why the node does this comparison.

## Activation

`vms/saevm/cchain/genesis.go` adds a `PrecompileUpgrade` keyed on the Helicon time and activates the account at
genesis (when Helicon is active at genesis) or in `StartExecutingBlock` at the first Helicon block. Before Helicon the
header carries no `PChainHeight`, so nothing is readable.
