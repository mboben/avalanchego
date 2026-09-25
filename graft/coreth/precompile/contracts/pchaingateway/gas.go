// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchaingateway

import "github.com/ava-labs/avalanchego/snow/pchain"

// Gas schedule and protocol caps. Gas is charged before any P-chain access and
// depends only on the call arguments plus, for owner addresses, on the
// returned data, so it is identical on every node. The schedule targets the
// ~100 Mgas/s of the Granite warp costs: a block read (height index + block
// fetch + decode, cached after the first read) and an input resolution (one
// producing-transaction fetch) are priced as database reads.
const (
	// GetPChainHeightGas is the cost of reading the header-committed height.
	GetPChainHeightGas uint64 = 200
	// BlockReadGas is charged by every call that reads a P-chain block.
	BlockReadGas uint64 = 20_000
	// TxGas is charged per requested transaction slot of getTransactions.
	TxGas uint64 = 5_000
	// InputGas is charged per requested input slot (inline inputs of
	// getTransactions, inputs of getTransactionInputs, the single input of
	// getStakeOwner).
	InputGas uint64 = 10_000
	// AddressGas is charged per returned owner address (per candidate
	// address for getStakeOwner) once the data is known.
	AddressGas uint64 = 200
	// ByteGas is charged per byte of P-chain data the node decodes for a
	// call: the stored bytes of the block read and of every producing
	// transaction resolved for an input. It is charged before the bytes are
	// parsed, and equally when the reader serves a cached copy, so gas never
	// depends on node-local caches. Calibration: parsing plus decoding runs at
	// ~65 MB/s (~15 ns/byte, transaction-dense blocks) on an M-series laptop
	// (BenchmarkParse* in vms/platformvm/pchainreader), i.e. ~1.5 gas/byte at
	// the 100 Mgas/s target; 10 gas/byte keeps a >6x margin for slower
	// validators. A 2 MiB block (the P-chain maximum) costs ~21M gas, a
	// 128 KiB block (the builder's target size) ~1.3M gas.
	ByteGas uint64 = 10
	// InputOrderGas is charged per input of the transaction when a non-empty
	// page is read in InputOrderByUTXOTxIDString order (getTransactionInputs
	// with order 1, getStakeOwner with order 1): whatever the page size, the
	// reader converts every input's UTXO transaction ID to its CB58 string
	// and sorts them. The reader caches the order per decoded block, but the
	// gas is charged on every such read. Calibration:
	// BenchmarkPermutationByString in vms/platformvm/pchainreader measures
	// 1.3-1.6 µs per input (16 to 20,000 inputs; the string conversion
	// dominates) on an M-series laptop, i.e. ~160 gas at the 100 Mgas/s
	// target; 1,000 gas/input keeps a >6x margin.
	InputOrderGas uint64 = 1_000

	// MaxTxsPerCall caps maxTxs of getTransactions.
	MaxTxsPerCall = 32
	// MaxInlineInputsPerTx caps maxInputsPerTx of getTransactions.
	MaxInlineInputsPerTx = 8
	// MaxInputsPerCall caps maxInputs of getTransactionInputs.
	MaxInputsPerCall = 64
)

// GetBlockGas is the cost of getBlock for a block of blockBytes stored bytes.
func GetBlockGas(blockBytes uint64) uint64 {
	return BlockReadGas + blockBytes*ByteGas
}

// GetTransactionsGas is the cost of getTransactions for the given page sizes,
// the total stored bytes decoded (the block plus the producing transactions
// of the inline inputs) and the number of owner addresses returned inline.
// Arguments must be within the caps.
func GetTransactionsGas(maxTxs, maxInputsPerTx uint32, decodedBytes uint64, addresses int) uint64 {
	return BlockReadGas +
		uint64(maxTxs)*TxGas +
		uint64(maxTxs)*uint64(maxInputsPerTx)*InputGas +
		decodedBytes*ByteGas +
		uint64(addresses)*AddressGas
}

// GetTransactionInputsGas is the cost of getTransactionInputs for the given
// page size, the total stored bytes decoded, the number of inputs ordered by
// CB58 string (the transaction's input count for a non-empty page in order 1,
// else 0) and the number of owner addresses returned.
func GetTransactionInputsGas(maxInputs uint32, decodedBytes uint64, orderedInputs uint32, addresses int) uint64 {
	return BlockReadGas + uint64(maxInputs)*InputGas + decodedBytes*ByteGas + uint64(orderedInputs)*InputOrderGas + uint64(addresses)*AddressGas
}

// GetStakeOwnerGas is the cost of getStakeOwner given the total stored bytes
// decoded, the number of inputs ordered by CB58 string (the transaction's
// input count in order 1, else 0) and the number of candidate owner addresses
// of the first input.
func GetStakeOwnerGas(decodedBytes uint64, orderedInputs uint32, candidates int) uint64 {
	return BlockReadGas + InputGas + decodedBytes*ByteGas + uint64(orderedInputs)*InputOrderGas + uint64(candidates)*AddressGas
}

// InputOrderingGas is the cost of ordering the inputs of a transaction with
// inputCount inputs before reading max of them from position from in order:
// InputOrderGas per input of the transaction when the order is by CB58 string
// and the page is not empty, else 0.
func InputOrderingGas(inputCount, from, max uint32, order pchain.InputOrder) uint64 {
	if order != pchain.InputOrderByUTXOTxIDString || max == 0 || from >= inputCount {
		return 0
	}
	return uint64(inputCount) * InputOrderGas
}
