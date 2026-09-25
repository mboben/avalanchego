// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

// Package pchain is the read-only, in-process view of accepted P-chain blocks
// and primary-network staking transactions that other chains consume, in
// particular the C-chain "P-chain gateway" precompile.
//
// It is a leaf package (it imports only ids) so that snow, the platformvm and
// coreth can all depend on it: the platformvm implements [Reader],
// chains/manager wires it into snow.Context.PChainReader and coreth's
// precompile only sees these plain types.
//
// Every value returned by a [Reader] is derived from accepted P-chain blocks
// only and is therefore identical on every node that has accepted the same
// height; callers are responsible for bounding the heights they read by a
// consensus-committed value.
package pchain

import (
	"errors"
	"sync"

	"github.com/ava-labs/avalanchego/ids"
)

var (
	// ErrNotAccepted is returned when a height above the local last accepted
	// P-chain height is requested.
	ErrNotAccepted          = errors.New("P-chain height not accepted yet")
	ErrTxIndexOutOfRange    = errors.New("transaction index out of range")
	ErrUnsupportedTx        = errors.New("unsupported transaction type")
	ErrInputIndexOutOfRange = errors.New("input index out of range")
	ErrInvalidInputOrder    = errors.New("invalid input order")
)

// BlockKind is the kind of an accepted P-chain block.
type BlockKind uint8

const (
	BlockStandard BlockKind = iota
	// BlockProposal blocks carry decision transactions followed by exactly
	// one proposal transaction (the last element of [Block.Txs]). The
	// proposal's outcome is the kind of the block at height+1: [BlockCommit]
	// or [BlockAbort].
	BlockProposal
	BlockCommit
	BlockAbort
	// BlockAtomic is the pre-Apricot-Phase-5 atomic block (historical only).
	BlockAtomic
)

// TxType identifies the decoded staking transaction types. Bit `t` of a type
// filter mask selects type `t`.
type TxType uint8

const (
	// TxUnsupported marks transactions the gateway does not decode. They are
	// still counted in [Block.Txs] so that indexes match the block's raw
	// transaction list.
	TxUnsupported TxType = iota
	TxAddPermissionlessValidator
	TxAddPermissionlessDelegator
	TxAddAutoRenewedValidator
	TxSetAutoRenewedValidatorConfig
	TxRewardAutoRenewedValidator
	// TxAddValidator and TxAddDelegator are the pre-Durango staking
	// transactions (historical blocks only).
	TxAddValidator
	TxAddDelegator

	// NumTxTypes is one past the highest [TxType].
	NumTxTypes
)

// InputOrder selects the order in which the inputs of a transaction are
// returned by [Reader.GetInputs].
type InputOrder uint8

const (
	// InputOrderCanonical is the serialized order of the transaction. The
	// P-chain requires inputs sorted by UTXO transaction ID bytes, then by
	// output index.
	InputOrderCanonical InputOrder = iota
	// InputOrderByUTXOTxIDString sorts by the CB58 string representation of
	// the UTXO transaction ID (ids.ID.String), then by output index. It
	// differs from the canonical order only when the CB58 strings have
	// different lengths.
	InputOrderByUTXOTxIDString
)

// Block is an accepted P-chain block.
type Block struct {
	Height   uint64
	ID       ids.ID
	ParentID ids.ID
	// Timestamp is the block time in Unix seconds; 0 for Apricot-era blocks,
	// which carry no timestamp.
	Timestamp uint64
	Kind      BlockKind
	// Txs has one entry per raw transaction of the block, in block order.
	Txs []Tx
}

// Tx is a decoded primary-network staking transaction. Fields that do not
// apply to a type are zero. All amounts are in nAVAX (nFLR), all times in
// Unix seconds.
type Tx struct {
	Type TxType
	// Index is the position in the block's raw transaction list.
	Index uint32
	ID    ids.ID

	NodeID ids.NodeID
	// StartTime is the effective start of the stake: the block timestamp when
	// Durango is active at that block (the chain time at acceptance), else
	// the transaction's own start field. For config and reward transactions
	// it is the block timestamp.
	StartTime uint64
	// EndTime is the stake end (bounded stakers), the end of the first cycle
	// (auto-renewed validators) or the cycle end (reward transactions).
	EndTime uint64
	// Period is the auto-renew cycle length; for config transactions the
	// next cycle length (0 requests an exit at the end of the cycle).
	Period uint64
	// Weight is the staked amount.
	Weight uint64
	// DelegationShares is the validator's delegation fee, times 10,000.
	DelegationShares uint32
	// AutoCompoundRewardShares is the auto-renewed validator's restake share
	// in millionths.
	AutoCompoundRewardShares uint32
	// StakerTxID references the AddAutoRenewedValidatorTx for config and
	// reward transactions.
	StakerTxID ids.ID
	// InputCount is the number of consumed inputs; see [Reader.GetInputs].
	InputCount uint32
}

// Input is a consumed UTXO of a staking transaction together with the owner of
// the consumed output, resolved from the transaction that produced it.
type Input struct {
	UTXOTxID        ids.ID
	UTXOOutputIndex uint32
	Amount          uint64
	// Resolved is false when the producing output could not be located; the
	// owner fields are then zero.
	Resolved bool
	// Genesis is true when the consumed output is a genesis UTXO, which has
	// no producing transaction (production genesis UTXOs carry the empty
	// transaction ID).
	Genesis   bool
	Locktime  uint64
	Threshold uint32
	// Addresses are all owners of the consumed output in on-chain (byte)
	// order.
	Addresses []ids.ShortID
	// SigIndices are the positions in Addresses whose signatures authorized
	// the spend.
	SigIndices []uint32
}

// ByteCharger accounts for the P-chain consensus bytes a read decodes: the
// stored bytes of the block being read ([Reader.GetBlock]) and of every
// producing transaction resolved for an input ([BlockReader.GetInputs]). It
// is called before the bytes are parsed, and with the same amount when the
// reader serves a cached copy, so that callers can charge gas that never
// depends on node-local cache state. Returning an error aborts the read; the
// error is returned unchanged. A nil ByteCharger charges nothing.
type ByteCharger func(bytes uint64) error

// Reader reads accepted P-chain blocks. Implementations are safe for
// concurrent use. Returned values must be treated as read-only.
type Reader interface {
	// LastAcceptedHeight returns the height of the last accepted P-chain
	// block of this node.
	LastAcceptedHeight() (uint64, error)
	// GetBlock returns the accepted block at height, or [ErrNotAccepted] if
	// the local P-chain has not accepted it yet. charge is called with the
	// block's stored size before it is decoded. The returned [BlockReader]
	// holds the decoded block, so reading its inputs never decodes (or
	// charges) the block again.
	GetBlock(height uint64, charge ByteCharger) (BlockReader, error)
}

// BlockReader is a decoded accepted block. It stays valid for as long as the
// caller holds it, independently of the [Reader]'s caches.
type BlockReader interface {
	// Block returns the decoded block.
	Block() *Block
	// GetInputs returns up to max inputs of the transaction at txIndex,
	// starting at position from of the requested order, and the total number
	// of inputs. Only decoded (non-[TxUnsupported]) transactions have
	// inputs. charge is called with the stored size of each producing
	// transaction that is resolved (genesis UTXOs have none) before it is
	// decoded.
	GetInputs(txIndex, from, max uint32, order InputOrder, charge ByteCharger) ([]Input, uint32, error)
}

// ReaderProvider is implemented by the P-chain VM. The returned Reader takes
// lock around every access to the P-chain state.
type ReaderProvider interface {
	NewPChainReader(lock sync.Locker) Reader
}
