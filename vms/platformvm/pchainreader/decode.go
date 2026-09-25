// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchainreader

import (
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/pchain"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/block"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
)

var (
	_ block.Visitor = (*kindVisitor)(nil)
	_ txs.Visitor   = (*txDecoder)(nil)
)

// decodedBlock is the cached, decoded form of an accepted block.
type decodedBlock struct {
	block pchain.Block
	// bytes is the stored size of the block, charged on every read.
	bytes uint64
	// ins holds the consumed inputs of every decoded transaction, indexed
	// like block.Txs; nil for unsupported transactions.
	ins [][]*avax.TransferableInput
	// byString caches, per transaction, the permutation of ins in
	// [pchain.InputOrderByUTXOTxIDString], computed once on first use.
	byString []stringOrder
}

// stringOrder lazily holds the CB58-string order of one transaction's inputs.
type stringOrder struct {
	once sync.Once
	perm []int
}

// inputsByString returns the indexes of transaction txIndex's inputs in
// CB58-string order, computing them on first use. Ordering converts every
// input's UTXO transaction ID to its string and sorts them, whatever page is
// read afterwards; the gateway prices that per input of the transaction.
func (db *decodedBlock) inputsByString(txIndex uint32) []int {
	so := &db.byString[txIndex]
	so.once.Do(func() {
		so.perm = permutationByString(db.ins[txIndex])
	})
	return so.perm
}

func (r *Reader) decode(blk block.Block) *decodedBlock {
	kind := &kindVisitor{}
	_ = blk.Visit(kind) // the visitor never fails

	var timestamp uint64
	if banff, ok := blk.(block.BanffBlock); ok {
		timestamp = uint64(banff.Timestamp().Unix())
	}
	// Apricot blocks (timestamp 0) predate Durango on every network.
	durango := r.upgrades.IsDurangoActivated(time.Unix(int64(timestamp), 0))

	rawTxs := blk.Txs()
	db := &decodedBlock{
		block: pchain.Block{
			Height:    blk.Height(),
			ID:        blk.ID(),
			ParentID:  blk.Parent(),
			Timestamp: timestamp,
			Kind:      kind.kind,
			Txs:       make([]pchain.Tx, len(rawTxs)),
		},
		ins:      make([][]*avax.TransferableInput, len(rawTxs)),
		byString: make([]stringOrder, len(rawTxs)),
	}
	for i, tx := range rawTxs {
		d := &txDecoder{
			blockTime: timestamp,
			durango:   durango,
			tx: pchain.Tx{
				Index: uint32(i),
				ID:    tx.ID(),
			},
		}
		if err := tx.Unsigned.Visit(d); err != nil {
			// Only a malformed auto-renewed node ID can fail, and accepted
			// transactions are syntactically verified; expose the transaction
			// as unsupported rather than failing the whole block.
			d.tx = pchain.Tx{Index: uint32(i), ID: tx.ID()}
			d.ins = nil
		}
		db.block.Txs[i] = d.tx
		db.ins[i] = d.ins
	}
	return db
}

// kindVisitor maps a block's concrete type to its [pchain.BlockKind].
type kindVisitor struct {
	kind pchain.BlockKind
}

func (v *kindVisitor) BanffAbortBlock(*block.BanffAbortBlock) error {
	v.kind = pchain.BlockAbort
	return nil
}

func (v *kindVisitor) BanffCommitBlock(*block.BanffCommitBlock) error {
	v.kind = pchain.BlockCommit
	return nil
}

func (v *kindVisitor) BanffProposalBlock(*block.BanffProposalBlock) error {
	v.kind = pchain.BlockProposal
	return nil
}

func (v *kindVisitor) BanffStandardBlock(*block.BanffStandardBlock) error {
	v.kind = pchain.BlockStandard
	return nil
}

func (v *kindVisitor) ApricotAbortBlock(*block.ApricotAbortBlock) error {
	v.kind = pchain.BlockAbort
	return nil
}

func (v *kindVisitor) ApricotCommitBlock(*block.ApricotCommitBlock) error {
	v.kind = pchain.BlockCommit
	return nil
}

func (v *kindVisitor) ApricotProposalBlock(*block.ApricotProposalBlock) error {
	v.kind = pchain.BlockProposal
	return nil
}

func (v *kindVisitor) ApricotStandardBlock(*block.ApricotStandardBlock) error {
	v.kind = pchain.BlockStandard
	return nil
}

func (v *kindVisitor) ApricotAtomicBlock(*block.ApricotAtomicBlock) error {
	v.kind = pchain.BlockAtomic
	return nil
}

// txDecoder decodes one transaction into a [pchain.Tx]. Transactions the
// gateway does not expose leave tx.Type at [pchain.TxUnsupported].
type txDecoder struct {
	blockTime uint64
	durango   bool

	tx  pchain.Tx
	ins []*avax.TransferableInput
}

// startTime applies the Durango rule: post-Durango stakers start at the chain
// time of the accepting block; before, at the transaction's start field.
func (d *txDecoder) startTime(txStart uint64) uint64 {
	if d.durango {
		return d.blockTime
	}
	return txStart
}

func (d *txDecoder) setInputs(ins []*avax.TransferableInput) {
	d.ins = ins
	d.tx.InputCount = uint32(len(ins))
}

func (d *txDecoder) boundedStaker(
	txType pchain.TxType,
	validator txs.Validator,
	delegationShares uint32,
	ins []*avax.TransferableInput,
) {
	d.tx.Type = txType
	d.tx.NodeID = validator.NodeID
	d.tx.StartTime = d.startTime(validator.Start)
	d.tx.EndTime = validator.End
	d.tx.Weight = validator.Wght
	d.tx.DelegationShares = delegationShares
	d.setInputs(ins)
}

// Apricot transactions.

func (d *txDecoder) AddValidatorTx(tx *txs.AddValidatorTx) error {
	d.boundedStaker(pchain.TxAddValidator, tx.Validator, tx.DelegationShares, tx.Ins)
	return nil
}

func (*txDecoder) AddSubnetValidatorTx(*txs.AddSubnetValidatorTx) error {
	return nil
}

func (d *txDecoder) AddDelegatorTx(tx *txs.AddDelegatorTx) error {
	d.boundedStaker(pchain.TxAddDelegator, tx.Validator, 0, tx.Ins)
	return nil
}

func (*txDecoder) CreateChainTx(*txs.CreateChainTx) error {
	return nil
}

func (*txDecoder) CreateSubnetTx(*txs.CreateSubnetTx) error {
	return nil
}

func (*txDecoder) ImportTx(*txs.ImportTx) error {
	return nil
}

func (*txDecoder) ExportTx(*txs.ExportTx) error {
	return nil
}

func (*txDecoder) AdvanceTimeTx(*txs.AdvanceTimeTx) error {
	return nil
}

func (*txDecoder) RewardValidatorTx(*txs.RewardValidatorTx) error {
	return nil
}

// Banff transactions.

func (*txDecoder) RemoveSubnetValidatorTx(*txs.RemoveSubnetValidatorTx) error {
	return nil
}

func (*txDecoder) TransformSubnetTx(*txs.TransformSubnetTx) error {
	return nil
}

func (d *txDecoder) AddPermissionlessValidatorTx(tx *txs.AddPermissionlessValidatorTx) error {
	if tx.Subnet != constants.PrimaryNetworkID {
		return nil
	}
	d.boundedStaker(pchain.TxAddPermissionlessValidator, tx.Validator, tx.DelegationShares, tx.Ins)
	return nil
}

func (d *txDecoder) AddPermissionlessDelegatorTx(tx *txs.AddPermissionlessDelegatorTx) error {
	if tx.Subnet != constants.PrimaryNetworkID {
		return nil
	}
	d.boundedStaker(pchain.TxAddPermissionlessDelegator, tx.Validator, 0, tx.Ins)
	return nil
}

// Durango transactions.

func (*txDecoder) TransferSubnetOwnershipTx(*txs.TransferSubnetOwnershipTx) error {
	return nil
}

func (*txDecoder) BaseTx(*txs.BaseTx) error {
	return nil
}

// Etna transactions.

func (*txDecoder) ConvertSubnetToL1Tx(*txs.ConvertSubnetToL1Tx) error {
	return nil
}

func (*txDecoder) RegisterL1ValidatorTx(*txs.RegisterL1ValidatorTx) error {
	return nil
}

func (*txDecoder) SetL1ValidatorWeightTx(*txs.SetL1ValidatorWeightTx) error {
	return nil
}

func (*txDecoder) IncreaseL1ValidatorBalanceTx(*txs.IncreaseL1ValidatorBalanceTx) error {
	return nil
}

func (*txDecoder) DisableL1ValidatorTx(*txs.DisableL1ValidatorTx) error {
	return nil
}

// Helicon transactions.

func (d *txDecoder) AddAutoRenewedValidatorTx(tx *txs.AddAutoRenewedValidatorTx) error {
	nodeID, err := ids.ToNodeID(tx.ValidatorNodeID)
	if err != nil {
		return err
	}
	d.tx.Type = pchain.TxAddAutoRenewedValidator
	d.tx.NodeID = nodeID
	d.tx.StartTime = d.blockTime
	d.tx.Period = tx.Period
	d.tx.EndTime = d.blockTime + tx.Period
	d.tx.Weight = tx.Weight()
	d.tx.DelegationShares = tx.DelegationShares
	d.tx.AutoCompoundRewardShares = tx.AutoCompoundRewardShares
	d.setInputs(tx.Ins)
	return nil
}

func (d *txDecoder) SetAutoRenewedValidatorConfigTx(tx *txs.SetAutoRenewedValidatorConfigTx) error {
	d.tx.Type = pchain.TxSetAutoRenewedValidatorConfig
	d.tx.StartTime = d.blockTime
	d.tx.Period = tx.Period
	d.tx.AutoCompoundRewardShares = tx.AutoCompoundRewardShares
	d.tx.StakerTxID = tx.TxID
	d.setInputs(tx.Ins)
	return nil
}

func (d *txDecoder) RewardAutoRenewedValidatorTx(tx *txs.RewardAutoRenewedValidatorTx) error {
	d.tx.Type = pchain.TxRewardAutoRenewedValidator
	d.tx.StartTime = d.blockTime
	d.tx.EndTime = tx.Timestamp
	d.tx.StakerTxID = tx.TxID
	return nil
}
