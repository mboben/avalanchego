// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchainreader

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/pchain"
	"github.com/ava-labs/avalanchego/upgrade"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/block"
	"github.com/ava-labs/avalanchego/vms/platformvm/signer"
	"github.com/ava-labs/avalanchego/vms/platformvm/stakeable"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/vms/types"
)

const (
	durangoTime     = 1_000_000
	preDurangoTime  = durangoTime - 10
	postDurangoTime = durangoTime + 10
)

var (
	testUpgrades = upgrade.Config{DurangoTime: time.Unix(durangoTime, 0)}
	avaxAssetID  = ids.GenerateTestID()
	addrA        = ids.ShortID{1}
	addrB        = ids.ShortID{2}
	addrC        = ids.ShortID{3}

	// genesisMarkerID stands in for a test genesis that gives its UTXOs an
	// arbitrary transaction ID instead of ids.Empty.
	genesisMarkerID = ids.ID{0x9e, 0x5e}
)

// fakeStore is an in-memory Store recording how often it is called.
type fakeStore struct {
	lastAccepted ids.ID
	heights      map[uint64]ids.ID
	blocks       map[ids.ID]block.Block
	txs          map[ids.ID]*txs.Tx
	rewardUTXOs  map[ids.ID][]*avax.UTXO

	calls       int // every store call
	heightCalls int // GetBlockIDAtHeight calls
	bytesCalls  int // GetBlockBytes and GetTxBytes calls
	// staleLastAccepted is returned by the next staleReads GetLastAccepted
	// calls, simulating a tip that moves between the reader's lookups.
	staleLastAccepted ids.ID
	staleReads        int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		heights:     map[uint64]ids.ID{},
		blocks:      map[ids.ID]block.Block{},
		txs:         map[ids.ID]*txs.Tx{},
		rewardUTXOs: map[ids.ID][]*avax.UTXO{},
	}
}

func (s *fakeStore) accept(blk block.Block) {
	s.heights[blk.Height()] = blk.ID()
	s.blocks[blk.ID()] = blk
	s.lastAccepted = blk.ID()
	for _, tx := range blk.Txs() {
		s.txs[tx.ID()] = tx
	}
}

func (s *fakeStore) GetLastAccepted() ids.ID {
	s.calls++
	if s.staleReads > 0 {
		s.staleReads--
		return s.staleLastAccepted
	}
	return s.lastAccepted
}

func (s *fakeStore) GetBlockIDAtHeight(height uint64) (ids.ID, error) {
	s.calls++
	s.heightCalls++
	id, ok := s.heights[height]
	if !ok {
		return ids.Empty, database.ErrNotFound
	}
	return id, nil
}

func (s *fakeStore) GetBlockBytes(blockID ids.ID) ([]byte, error) {
	s.calls++
	s.bytesCalls++
	blk, ok := s.blocks[blockID]
	if !ok {
		return nil, database.ErrNotFound
	}
	return blk.Bytes(), nil
}

func (s *fakeStore) GetTxBytes(txID ids.ID) ([]byte, error) {
	s.calls++
	s.bytesCalls++
	tx, ok := s.txs[txID]
	if !ok {
		return nil, database.ErrNotFound
	}
	return tx.Bytes(), nil
}

func (s *fakeStore) GetRewardUTXOs(txID ids.ID) ([]*avax.UTXO, error) {
	s.calls++
	return s.rewardUTXOs[txID], nil
}

func genesisList(utxos ...*avax.UTXO) func() []*avax.UTXO {
	return func() []*avax.UTXO { return utxos }
}

// countingLocker records Lock/Unlock calls.
type countingLocker struct {
	locks, unlocks int
}

func (l *countingLocker) Lock()   { l.locks++ }
func (l *countingLocker) Unlock() { l.unlocks++ }

func signedTx(t *testing.T, unsigned txs.UnsignedTx) *txs.Tx {
	t.Helper()
	tx, err := txs.NewSigned(unsigned, txs.Codec, nil)
	require.NoError(t, err)
	return tx
}

func owners(addrs ...ids.ShortID) secp256k1fx.OutputOwners {
	return secp256k1fx.OutputOwners{Threshold: 1, Addrs: addrs}
}

func transferOutput(amount uint64, o secp256k1fx.OutputOwners) *avax.TransferableOutput {
	return &avax.TransferableOutput{
		Asset: avax.Asset{ID: avaxAssetID},
		Out:   &secp256k1fx.TransferOutput{Amt: amount, OutputOwners: o},
	}
}

func transferInput(utxoTxID ids.ID, index uint32, amount uint64, sigIndices ...uint32) *avax.TransferableInput {
	return &avax.TransferableInput{
		UTXOID: avax.UTXOID{TxID: utxoTxID, OutputIndex: index},
		Asset:  avax.Asset{ID: avaxAssetID},
		In: &secp256k1fx.TransferInput{
			Amt:   amount,
			Input: secp256k1fx.Input{SigIndices: sigIndices},
		},
	}
}

func baseTx(ins []*avax.TransferableInput, outs []*avax.TransferableOutput) txs.BaseTx {
	return txs.BaseTx{BaseTx: avax.BaseTx{
		NetworkID:    constants.UnitTestID,
		BlockchainID: constants.PlatformChainID,
		Ins:          ins,
		Outs:         outs,
		Memo:         types.JSONByteSlice{},
	}}
}

func permissionlessValidator(subnetID ids.ID, nodeID ids.NodeID, ins []*avax.TransferableInput) *txs.AddPermissionlessValidatorTx {
	return &txs.AddPermissionlessValidatorTx{
		BaseTx:                baseTx(ins, nil),
		Validator:             txs.Validator{NodeID: nodeID, Start: 111, End: 222, Wght: 1000},
		Subnet:                subnetID,
		Signer:                &signer.Empty{},
		StakeOuts:             []*avax.TransferableOutput{transferOutput(1000, owners(addrA))},
		ValidatorRewardsOwner: &secp256k1fx.OutputOwners{},
		DelegatorRewardsOwner: &secp256k1fx.OutputOwners{},
		DelegationShares:      20_000,
	}
}

func permissionlessDelegator(subnetID ids.ID, nodeID ids.NodeID, ins []*avax.TransferableInput) *txs.AddPermissionlessDelegatorTx {
	return &txs.AddPermissionlessDelegatorTx{
		BaseTx:                 baseTx(ins, nil),
		Validator:              txs.Validator{NodeID: nodeID, Start: 333, End: 444, Wght: 500},
		Subnet:                 subnetID,
		StakeOuts:              []*avax.TransferableOutput{transferOutput(500, owners(addrB))},
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{},
	}
}

func autoRenewedValidator(nodeID ids.NodeID, ins []*avax.TransferableInput) *txs.AddAutoRenewedValidatorTx {
	return &txs.AddAutoRenewedValidatorTx{
		BaseTx:                   baseTx(ins, nil),
		ValidatorNodeID:          nodeID.Bytes(),
		Signer:                   &signer.Empty{},
		StakeOuts:                []*avax.TransferableOutput{transferOutput(700, owners(addrA)), transferOutput(300, owners(addrB))},
		ValidatorRewardsOwner:    &secp256k1fx.OutputOwners{},
		DelegatorRewardsOwner:    &secp256k1fx.OutputOwners{},
		ValidatorAuthority:       &secp256k1fx.OutputOwners{},
		DelegationShares:         30_000,
		AutoCompoundRewardShares: 250_000,
		Period:                   90 * 24 * 3600,
	}
}

func importTx(ins []*avax.TransferableInput) *txs.ImportTx {
	return &txs.ImportTx{
		BaseTx:      baseTx(ins, nil),
		SourceChain: ids.GenerateTestID(),
		ImportedInputs: []*avax.TransferableInput{
			transferInput(ids.GenerateTestID(), 0, 1, 0),
		},
	}
}

func newBanffStandard(t *testing.T, timestamp int64, parent ids.ID, height uint64, blkTxs ...*txs.Tx) *block.BanffStandardBlock {
	t.Helper()
	blk, err := block.NewBanffStandardBlock(time.Unix(timestamp, 0), parent, height, blkTxs)
	require.NoError(t, err)
	return blk
}

func TestDecodeBanffStandardBlock(t *testing.T) {
	require := require.New(t)

	nodeA, nodeB, nodeC := ids.GenerateTestNodeID(), ids.GenerateTestNodeID(), ids.GenerateTestNodeID()
	fundingTxID := ids.GenerateTestID()
	validatorTx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, nodeA, []*avax.TransferableInput{
		transferInput(fundingTxID, 0, 1000, 0),
		transferInput(fundingTxID, 1, 1, 0),
	}))
	delegatorTx := signedTx(t, permissionlessDelegator(constants.PrimaryNetworkID, nodeA, []*avax.TransferableInput{
		transferInput(fundingTxID, 2, 500, 0),
	}))
	subnetValidatorTx := signedTx(t, permissionlessValidator(ids.GenerateTestID(), nodeB, []*avax.TransferableInput{
		transferInput(fundingTxID, 3, 1000, 0),
	}))
	autoRenewedTx := signedTx(t, autoRenewedValidator(nodeC, []*avax.TransferableInput{
		transferInput(fundingTxID, 4, 1000, 0),
	}))
	autoRenewedTxID := autoRenewedTx.ID()
	configTx := signedTx(t, &txs.SetAutoRenewedValidatorConfigTx{
		BaseTx:                   baseTx([]*avax.TransferableInput{transferInput(fundingTxID, 5, 1, 0)}, nil),
		TxID:                     autoRenewedTxID,
		Auth:                     &secp256k1fx.Input{},
		AutoCompoundRewardShares: 1_000_000,
		Period:                   0,
	})
	unsupportedTx := signedTx(t, importTx([]*avax.TransferableInput{transferInput(fundingTxID, 6, 1, 0)}))

	parent := ids.GenerateTestID()
	blk := newBanffStandard(t, postDurangoTime, parent, 7, validatorTx, delegatorTx, subnetValidatorTx, autoRenewedTx, configTx, unsupportedTx)

	store := newFakeStore()
	store.accept(blk)
	reader := New(store, nil, testUpgrades)

	got, err := getBlock(reader, 7, nil)
	require.NoError(err)
	require.Equal(uint64(7), got.Height)
	require.Equal(blk.ID(), got.ID)
	require.Equal(parent, got.ParentID)
	require.Equal(uint64(postDurangoTime), got.Timestamp)
	require.Equal(pchain.BlockStandard, got.Kind)
	require.Len(got.Txs, 6)

	require.Equal(pchain.Tx{
		Type:             pchain.TxAddPermissionlessValidator,
		Index:            0,
		ID:               validatorTx.ID(),
		NodeID:           nodeA,
		StartTime:        postDurangoTime, // Durango: chain time, not the tx's start
		EndTime:          222,
		Weight:           1000,
		DelegationShares: 20_000,
		InputCount:       2,
	}, got.Txs[0])
	require.Equal(pchain.Tx{
		Type:       pchain.TxAddPermissionlessDelegator,
		Index:      1,
		ID:         delegatorTx.ID(),
		NodeID:     nodeA,
		StartTime:  postDurangoTime,
		EndTime:    444,
		Weight:     500,
		InputCount: 1,
	}, got.Txs[1])
	// A subnet staker is not a primary-network staker: skipped but counted.
	require.Equal(pchain.Tx{Type: pchain.TxUnsupported, Index: 2, ID: subnetValidatorTx.ID()}, got.Txs[2])
	require.Equal(pchain.Tx{
		Type:                     pchain.TxAddAutoRenewedValidator,
		Index:                    3,
		ID:                       autoRenewedTxID,
		NodeID:                   nodeC,
		StartTime:                postDurangoTime,
		EndTime:                  postDurangoTime + 90*24*3600,
		Period:                   90 * 24 * 3600,
		Weight:                   1000, // sum of the stake outputs
		DelegationShares:         30_000,
		AutoCompoundRewardShares: 250_000,
		InputCount:               1,
	}, got.Txs[3])
	require.Equal(pchain.Tx{
		Type:                     pchain.TxSetAutoRenewedValidatorConfig,
		Index:                    4,
		ID:                       configTx.ID(),
		StartTime:                postDurangoTime,
		Period:                   0,
		AutoCompoundRewardShares: 1_000_000,
		StakerTxID:               autoRenewedTxID,
		InputCount:               1,
	}, got.Txs[4])
	require.Equal(pchain.Tx{Type: pchain.TxUnsupported, Index: 5, ID: unsupportedTx.ID()}, got.Txs[5])

	// Decoded blocks are cached: a second read makes no store calls.
	calls := store.calls
	again, err := getBlock(reader, 7, nil)
	require.NoError(err)
	require.Equal(got, again)
	require.Equal(calls, store.calls)
}

func TestDecodeProposalAndOptionBlocks(t *testing.T) {
	require := require.New(t)

	nodeID := ids.GenerateTestNodeID()
	fundingTxID := ids.GenerateTestID()
	stakerTxID := ids.GenerateTestID()
	decisionTx := signedTx(t, permissionlessDelegator(constants.PrimaryNetworkID, nodeID, []*avax.TransferableInput{
		transferInput(fundingTxID, 0, 500, 0),
	}))
	rewardTx := signedTx(t, &txs.RewardAutoRenewedValidatorTx{TxID: stakerTxID, Timestamp: postDurangoTime})

	genesisID := ids.GenerateTestID()
	proposal, err := block.NewBanffProposalBlock(time.Unix(postDurangoTime, 0), genesisID, 1, rewardTx, []*txs.Tx{decisionTx})
	require.NoError(err)
	commit, err := block.NewBanffCommitBlock(time.Unix(postDurangoTime, 0), proposal.ID(), 2)
	require.NoError(err)
	abort, err := block.NewBanffAbortBlock(time.Unix(postDurangoTime, 0), proposal.ID(), 3)
	require.NoError(err)

	store := newFakeStore()
	store.accept(proposal)
	store.accept(commit)
	store.accept(abort) // not a valid chain, but exercises the kind mapping
	reader := New(store, nil, testUpgrades)

	got, err := getBlock(reader, 1, nil)
	require.NoError(err)
	require.Equal(pchain.BlockProposal, got.Kind)
	require.Len(got.Txs, 2)
	require.Equal(pchain.TxAddPermissionlessDelegator, got.Txs[0].Type)
	// The proposal transaction is always last.
	require.Equal(pchain.Tx{
		Type:       pchain.TxRewardAutoRenewedValidator,
		Index:      1,
		ID:         rewardTx.ID(),
		StartTime:  postDurangoTime,
		EndTime:    postDurangoTime,
		StakerTxID: stakerTxID,
	}, got.Txs[1])

	got, err = getBlock(reader, 2, nil)
	require.NoError(err)
	require.Equal(pchain.BlockCommit, got.Kind)
	require.Empty(got.Txs)

	got, err = getBlock(reader, 3, nil)
	require.NoError(err)
	require.Equal(pchain.BlockAbort, got.Kind)
	require.Empty(got.Txs)

	// The reward transaction has no inputs.
	inputs, total, err := getInputs(reader, 1, 1, 0, 10, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Zero(total)
	require.Empty(inputs)
}

func TestDecodeApricotBlocks(t *testing.T) {
	require := require.New(t)

	nodeID := ids.GenerateTestNodeID()
	fundingTxID := ids.GenerateTestID()
	legacyValidator := signedTx(t, &txs.AddValidatorTx{
		BaseTx:           baseTx([]*avax.TransferableInput{transferInput(fundingTxID, 0, 2000, 0)}, nil),
		Validator:        txs.Validator{NodeID: nodeID, Start: 555, End: 666, Wght: 2000},
		StakeOuts:        []*avax.TransferableOutput{transferOutput(2000, owners(addrA))},
		RewardsOwner:     &secp256k1fx.OutputOwners{},
		DelegationShares: 50_000,
	})
	legacyDelegator := signedTx(t, &txs.AddDelegatorTx{
		BaseTx:                 baseTx([]*avax.TransferableInput{transferInput(fundingTxID, 1, 25, 0)}, nil),
		Validator:              txs.Validator{NodeID: nodeID, Start: 777, End: 888, Wght: 25},
		StakeOuts:              []*avax.TransferableOutput{transferOutput(25, owners(addrB))},
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{},
	})
	atomicTx := signedTx(t, importTx([]*avax.TransferableInput{transferInput(fundingTxID, 2, 1, 0)}))

	genesisID := ids.GenerateTestID()
	proposal, err := block.NewApricotProposalBlock(genesisID, 1, legacyValidator)
	require.NoError(err)
	commit, err := block.NewApricotCommitBlock(proposal.ID(), 2)
	require.NoError(err)
	standard, err := block.NewApricotStandardBlock(commit.ID(), 3, []*txs.Tx{legacyDelegator})
	require.NoError(err)
	atomic, err := block.NewApricotAtomicBlock(standard.ID(), 4, atomicTx)
	require.NoError(err)
	abort, err := block.NewApricotAbortBlock(atomic.ID(), 5)
	require.NoError(err)

	store := newFakeStore()
	for _, blk := range []block.Block{proposal, commit, standard, atomic, abort} {
		store.accept(blk)
	}
	reader := New(store, nil, testUpgrades)

	got, err := getBlock(reader, 1, nil)
	require.NoError(err)
	require.Equal(pchain.BlockProposal, got.Kind)
	require.Zero(got.Timestamp) // Apricot blocks carry no timestamp
	require.Equal(pchain.Tx{
		Type:             pchain.TxAddValidator,
		Index:            0,
		ID:               legacyValidator.ID(),
		NodeID:           nodeID,
		StartTime:        555, // pre-Durango: the transaction's own start
		EndTime:          666,
		Weight:           2000,
		DelegationShares: 50_000,
		InputCount:       1,
	}, got.Txs[0])

	got, err = getBlock(reader, 2, nil)
	require.NoError(err)
	require.Equal(pchain.BlockCommit, got.Kind)

	got, err = getBlock(reader, 3, nil)
	require.NoError(err)
	require.Equal(pchain.BlockStandard, got.Kind)
	require.Equal(pchain.Tx{
		Type:       pchain.TxAddDelegator,
		Index:      0,
		ID:         legacyDelegator.ID(),
		NodeID:     nodeID,
		StartTime:  777,
		EndTime:    888,
		Weight:     25,
		InputCount: 1,
	}, got.Txs[0])

	got, err = getBlock(reader, 4, nil)
	require.NoError(err)
	require.Equal(pchain.BlockAtomic, got.Kind)
	require.Equal(pchain.TxUnsupported, got.Txs[0].Type)

	got, err = getBlock(reader, 5, nil)
	require.NoError(err)
	require.Equal(pchain.BlockAbort, got.Kind)
}

func TestPreDurangoBanffStartTime(t *testing.T) {
	require := require.New(t)

	nodeID := ids.GenerateTestNodeID()
	validatorTx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, nodeID, []*avax.TransferableInput{
		transferInput(ids.GenerateTestID(), 0, 1000, 0),
	}))
	blk := newBanffStandard(t, preDurangoTime, ids.GenerateTestID(), 1, validatorTx)

	store := newFakeStore()
	store.accept(blk)
	reader := New(store, nil, testUpgrades)

	got, err := getBlock(reader, 1, nil)
	require.NoError(err)
	require.Equal(uint64(111), got.Txs[0].StartTime)
}

func TestGetBlockErrors(t *testing.T) {
	require := require.New(t)

	store := newFakeStore()
	acceptChain(t, store, 5)
	reader := New(store, nil, testUpgrades)

	last, err := reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(5), last)

	_, err = getBlock(reader, 6, nil)
	require.ErrorIs(err, pchain.ErrNotAccepted)

	// A height below the tip that is missing from the index is a database
	// problem, not a not-yet-accepted height.
	delete(store.heights, 4)
	_, err = getBlock(reader, 4, nil)
	require.ErrorIs(err, database.ErrNotFound)
	require.NotErrorIs(err, pchain.ErrNotAccepted)
	require.ErrorContains(err, "last accepted 5")

	_, _, err = getInputs(reader, 5, 0, 0, 1, pchain.InputOrderCanonical, nil)
	require.ErrorIs(err, pchain.ErrTxIndexOutOfRange)

	_, _, err = getInputs(reader, 5, 0, 0, 1, pchain.InputOrderByUTXOTxIDString+1, nil)
	require.ErrorIs(err, pchain.ErrInvalidInputOrder)
}

// inputFixture builds a staking transaction whose inputs exercise every owner
// resolution rule.
type inputFixture struct {
	store      *fakeStore
	reader     *Reader
	height     uint64
	stakingTx  *txs.Tx
	producerID ids.ID // BaseTx outputs
	stakerID   ids.ID // returned stake + reward UTXO
	missingID  ids.ID // no producing tx in the store
}

func newInputFixture(t *testing.T) *inputFixture {
	t.Helper()
	require := require.New(t)

	// A plain transaction producing two outputs, the second one locked.
	producer := signedTx(t, &txs.BaseTx{BaseTx: avax.BaseTx{
		NetworkID:    constants.UnitTestID,
		BlockchainID: constants.PlatformChainID,
		Ins:          []*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 5, 0)},
		Outs: []*avax.TransferableOutput{
			transferOutput(10, owners(addrA)),
			{
				Asset: avax.Asset{ID: avaxAssetID},
				Out: &stakeable.LockOut{
					Locktime: 99,
					TransferableOut: &secp256k1fx.TransferOutput{
						Amt:          20,
						OutputOwners: secp256k1fx.OutputOwners{Locktime: 7, Threshold: 2, Addrs: []ids.ShortID{addrA, addrB, addrC}},
					},
				},
			},
		},
		Memo: types.JSONByteSlice{},
	}})
	// A staker with one BaseTx output and two stake outputs; its reward UTXO
	// is stored separately, keyed by the staker tx.
	staker := signedTx(t, &txs.AddPermissionlessValidatorTx{
		BaseTx:                baseTx([]*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 5, 0)}, []*avax.TransferableOutput{transferOutput(1, owners(addrC))}),
		Validator:             txs.Validator{NodeID: ids.GenerateTestNodeID(), Start: 1, End: 2, Wght: 300},
		Subnet:                constants.PrimaryNetworkID,
		Signer:                &signer.Empty{},
		StakeOuts:             []*avax.TransferableOutput{transferOutput(100, owners(addrA)), transferOutput(200, owners(addrB))},
		ValidatorRewardsOwner: &secp256k1fx.OutputOwners{},
		DelegatorRewardsOwner: &secp256k1fx.OutputOwners{},
	})
	stakerID := staker.ID()

	genesisUTXO := &avax.UTXO{
		UTXOID: avax.UTXOID{TxID: ids.Empty, OutputIndex: 3},
		Asset:  avax.Asset{ID: avaxAssetID},
		Out:    &secp256k1fx.TransferOutput{Amt: 42, OutputOwners: owners(addrC)},
	}
	oddGenesisUTXO := &avax.UTXO{
		UTXOID: avax.UTXOID{TxID: genesisMarkerID, OutputIndex: 0},
		Asset:  avax.Asset{ID: avaxAssetID},
		Out:    &secp256k1fx.TransferOutput{Amt: 7, OutputOwners: owners(addrB)},
	}

	missingID := ids.GenerateTestID()
	ins := []*avax.TransferableInput{
		transferInput(ids.Empty, 3, 42, 0),        // genesis
		transferInput(producer.ID(), 0, 10, 0),    // BaseTx output
		transferInput(producer.ID(), 1, 20, 0, 2), // locked BaseTx output, signed by slots 0 and 2
		transferInput(producer.ID(), 5, 1, 0),     // no such output
		transferInput(stakerID, 1, 100, 0),        // returned stake (index len(Outs)+0)
		transferInput(stakerID, 3, 9, 0),          // reward UTXO (index len(Outs)+len(Stake)+0)
		transferInput(genesisMarkerID, 0, 7, 0),   // genesis UTXO with a non-empty tx id
		transferInput(missingID, 0, 1, 0),         // producing tx unknown
	}
	// The last input is wrapped in a stakeable lock.
	ins[4] = &avax.TransferableInput{
		UTXOID: ins[4].UTXOID,
		Asset:  ins[4].Asset,
		In:     &stakeable.LockIn{Locktime: 99, TransferableIn: ins[4].In},
	}
	stakingTx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, ids.GenerateTestNodeID(), ins))

	store := newFakeStore()
	store.txs[producer.ID()] = producer
	store.txs[stakerID] = staker
	store.rewardUTXOs[stakerID] = []*avax.UTXO{{
		UTXOID: avax.UTXOID{TxID: stakerID, OutputIndex: 3},
		Asset:  avax.Asset{ID: avaxAssetID},
		Out:    &secp256k1fx.TransferOutput{Amt: 9, OutputOwners: owners(addrB)},
	}}
	blk := newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1, stakingTx)
	store.accept(blk)
	require.NotNil(store.txs[stakingTx.ID()])

	return &inputFixture{
		store:      store,
		reader:     New(store, genesisList(genesisUTXO, oddGenesisUTXO), testUpgrades),
		height:     1,
		stakingTx:  stakingTx,
		producerID: producer.ID(),
		stakerID:   stakerID,
		missingID:  missingID,
	}
}

func TestGetInputsResolvesOwners(t *testing.T) {
	require := require.New(t)
	f := newInputFixture(t)

	// The unknown producing transaction is a local error, not an unresolved input.
	_, total, err := getInputs(f.reader, f.height, 0, 0, 10, pchain.InputOrderCanonical, nil)
	require.ErrorIs(err, database.ErrNotFound)
	require.Equal(uint32(8), total)

	inputs, total, err := getInputs(f.reader, f.height, 0, 0, 7, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Equal(uint32(8), total)
	require.Equal([]pchain.Input{
		{UTXOTxID: ids.Empty, UTXOOutputIndex: 3, Amount: 42, Resolved: true, Genesis: true, Threshold: 1, Addresses: []ids.ShortID{addrC}, SigIndices: []uint32{0}},
		{UTXOTxID: f.producerID, UTXOOutputIndex: 0, Amount: 10, Resolved: true, Threshold: 1, Addresses: []ids.ShortID{addrA}, SigIndices: []uint32{0}},
		{UTXOTxID: f.producerID, UTXOOutputIndex: 1, Amount: 20, Resolved: true, Locktime: 7, Threshold: 2, Addresses: []ids.ShortID{addrA, addrB, addrC}, SigIndices: []uint32{0, 2}},
		{UTXOTxID: f.producerID, UTXOOutputIndex: 5, Amount: 1, SigIndices: []uint32{0}},
		{UTXOTxID: f.stakerID, UTXOOutputIndex: 1, Amount: 100, Resolved: true, Threshold: 1, Addresses: []ids.ShortID{addrA}, SigIndices: []uint32{0}},
		{UTXOTxID: f.stakerID, UTXOOutputIndex: 3, Amount: 9, Resolved: true, Threshold: 1, Addresses: []ids.ShortID{addrB}, SigIndices: []uint32{0}},
		{UTXOTxID: genesisMarkerID, UTXOOutputIndex: 0, Amount: 7, Resolved: true, Genesis: true, Threshold: 1, Addresses: []ids.ShortID{addrB}, SigIndices: []uint32{0}},
	}, inputs)

	// Paging.
	inputs, total, err = getInputs(f.reader, f.height, 0, 4, 2, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Equal(uint32(8), total)
	require.Len(inputs, 2)
	require.Equal(f.stakerID, inputs[0].UTXOTxID)
	require.Equal(uint32(3), inputs[1].UTXOOutputIndex)

	inputs, _, err = getInputs(f.reader, f.height, 0, 7, 0, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Empty(inputs)

	inputs, _, err = getInputs(f.reader, f.height, 0, 8, 5, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Empty(inputs)

	_, _, err = getInputs(f.reader, f.height, 0, 9, 1, pchain.InputOrderCanonical, nil)
	require.ErrorIs(err, pchain.ErrInputIndexOutOfRange)

	// Owners are cached after the first resolution.
	calls := f.store.calls
	_, _, err = getInputs(f.reader, f.height, 0, 0, 7, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Equal(calls, f.store.calls)
}

func TestGetInputsUnsupportedTx(t *testing.T) {
	require := require.New(t)

	tx := signedTx(t, importTx([]*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 1, 0)}))
	blk := newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1, tx)
	store := newFakeStore()
	store.accept(blk)
	reader := New(store, nil, testUpgrades)

	_, _, err := getInputs(reader, 1, 0, 0, 1, pchain.InputOrderCanonical, nil)
	require.ErrorIs(err, pchain.ErrUnsupportedTx)
}

func TestGetInputsOrderByUTXOTxIDString(t *testing.T) {
	require := require.New(t)

	// byteFirst sorts before stringFirst by bytes but after it by CB58 string:
	// a 36-byte value below 58^48 has 48 base58 digits starting with a high
	// digit, while a value above it has 49 digits starting with '2'.
	byteFirst := ids.ID{0x01}
	stringFirst := ids.ID{0x03}
	require.Negative(bytes.Compare(byteFirst[:], stringFirst[:]))
	require.Greater(byteFirst.String(), stringFirst.String())

	producer := func(t *testing.T, id ids.ID, amounts ...uint64) *txs.Tx {
		outs := make([]*avax.TransferableOutput, len(amounts))
		for i, amount := range amounts {
			outs[i] = transferOutput(amount, owners(addrA))
		}
		tx := signedTx(t, &txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    constants.UnitTestID,
			BlockchainID: constants.PlatformChainID,
			Ins:          []*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 1, 0)},
			Outs:         outs,
			Memo:         types.JSONByteSlice{},
		}})
		// Force the transaction ID so that the inputs can reference it.
		tx.TxID = id
		return tx
	}

	store := newFakeStore()
	store.txs[byteFirst] = producer(t, byteFirst, 1, 2)
	store.txs[stringFirst] = producer(t, stringFirst, 3)

	// Canonical (serialized) order: by UTXO tx ID bytes, then output index.
	ins := []*avax.TransferableInput{
		transferInput(byteFirst, 0, 1, 0),
		transferInput(byteFirst, 1, 2, 0),
		transferInput(stringFirst, 0, 3, 0),
	}
	stakingTx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, ids.GenerateTestNodeID(), ins))
	blk := newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1, stakingTx)
	store.accept(blk)
	reader := New(store, nil, testUpgrades)

	canonical, total, err := getInputs(reader, 1, 0, 0, 3, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Equal(uint32(3), total)
	require.Equal([]uint64{1, 2, 3}, amounts(canonical))

	byString, total, err := getInputs(reader, 1, 0, 0, 3, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Equal(uint32(3), total)
	require.Equal([]uint64{3, 1, 2}, amounts(byString))
	require.Equal(stringFirst, byString[0].UTXOTxID)

	// Paging applies after ordering.
	page, _, err := getInputs(reader, 1, 0, 1, 1, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Equal([]uint64{1}, amounts(page))
}

func amounts(inputs []pchain.Input) []uint64 {
	out := make([]uint64, len(inputs))
	for i, in := range inputs {
		out[i] = in.Amount
	}
	return out
}

func TestLockedStoreHoldsLockPerCall(t *testing.T) {
	require := require.New(t)
	f := newInputFixture(t)

	locker := &countingLocker{}
	reader := New(NewLockedStore(locker, f.store), nil, testUpgrades)

	_, err := getBlock(reader, f.height, nil)
	require.NoError(err)
	_, _, err = getInputs(reader, f.height, 0, 1, 5, pchain.InputOrderCanonical, nil)
	require.NoError(err)

	require.Positive(locker.locks)
	require.Equal(locker.locks, locker.unlocks)
}

// recordingCharger records every charged byte count and can fail.
type recordingCharger struct {
	charges []uint64
	fail    error
}

func (c *recordingCharger) charge(bytes uint64) error {
	c.charges = append(c.charges, bytes)
	return c.fail
}

func TestGetBlockChargesStoredBytesOnMissAndHit(t *testing.T) {
	require := require.New(t)

	tx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, ids.GenerateTestNodeID(), []*avax.TransferableInput{
		transferInput(ids.GenerateTestID(), 0, 1000, 0),
	}))
	blk := newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1, tx)
	store := newFakeStore()
	store.accept(blk)
	reader := New(store, nil, testUpgrades)
	size := uint64(len(blk.Bytes()))

	miss := &recordingCharger{}
	_, err := getBlock(reader, 1, miss.charge)
	require.NoError(err)
	require.Equal([]uint64{size}, miss.charges)

	// A cached read charges exactly the same amount without touching the store.
	calls := store.calls
	hit := &recordingCharger{}
	_, err = getBlock(reader, 1, hit.charge)
	require.NoError(err)
	require.Equal([]uint64{size}, hit.charges)
	require.Equal(calls, store.calls)
}

func TestGetBlockChargeFailureAbortsBeforeDecoding(t *testing.T) {
	require := require.New(t)

	blk := newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1)
	store := newFakeStore()
	store.accept(blk)
	reader := New(store, nil, testUpgrades)

	errNoGas := errors.New("out of gas")
	failing := &recordingCharger{fail: errNoGas}
	_, err := getBlock(reader, 1, failing.charge)
	require.ErrorIs(err, errNoGas)
	require.Equal([]uint64{uint64(len(blk.Bytes()))}, failing.charges)

	// Nothing was cached: the next read goes back to the store.
	calls := store.calls
	_, err = getBlock(reader, 1, nil)
	require.NoError(err)
	require.Greater(store.calls, calls)
}

func TestGetInputsChargesProducingTxBytes(t *testing.T) {
	require := require.New(t)
	f := newInputFixture(t)

	producer := f.store.txs[f.producerID]
	staker := f.store.txs[f.stakerID]
	blockSize := uint64(len(f.store.blocks[f.store.heights[f.height]].Bytes()))

	// Inputs 0..2: genesis (no producing tx, nothing charged), then two
	// outputs of the same producing tx (charged on each resolution, the
	// second one from cache).
	c := &recordingCharger{}
	inputs, _, err := getInputs(f.reader, f.height, 0, 0, 3, pchain.InputOrderCanonical, c.charge)
	require.NoError(err)
	require.Len(inputs, 3)
	require.Equal([]uint64{blockSize, uint64(len(producer.Bytes())), uint64(len(producer.Bytes()))}, c.charges)

	// Returned stake and reward UTXO both parse the staker transaction.
	c = &recordingCharger{}
	_, _, err = getInputs(f.reader, f.height, 0, 4, 2, pchain.InputOrderCanonical, c.charge)
	require.NoError(err)
	require.Equal([]uint64{blockSize, uint64(len(staker.Bytes())), uint64(len(staker.Bytes()))}, c.charges)

	// Reading through the block handle charges the producing transactions
	// only: the block was paid for by GetBlock.
	br, err := f.reader.GetBlock(f.height, nil)
	require.NoError(err)
	c = &recordingCharger{}
	_, _, err = br.GetInputs(0, 0, 3, pchain.InputOrderCanonical, c.charge)
	require.NoError(err)
	require.Equal([]uint64{uint64(len(producer.Bytes())), uint64(len(producer.Bytes()))}, c.charges)

	// A failing charge on a producing tx aborts the page.
	errNoGas := errors.New("out of gas")
	var charged int
	failSecond := func(uint64) error {
		charged++
		if charged > 1 {
			return errNoGas
		}
		return nil
	}
	_, _, err = getInputs(f.reader, f.height, 0, 0, 3, pchain.InputOrderCanonical, failSecond)
	require.ErrorIs(err, errNoGas)
	require.Equal(2, charged) // block bytes, then the first producing tx
}

// getBlock returns the decoded block at height.
func getBlock(r *Reader, height uint64, charge pchain.ByteCharger) (*pchain.Block, error) {
	br, err := r.GetBlock(height, charge)
	if err != nil {
		return nil, err
	}
	return br.Block(), nil
}

// getInputs reads the block at height and then a page of the inputs of its
// transaction txIndex, charging the block and then the producing transactions
// through charge, exactly like a single gateway call does.
func getInputs(r *Reader, height uint64, txIndex, from, max uint32, order pchain.InputOrder, charge pchain.ByteCharger) ([]pchain.Input, uint32, error) {
	br, err := r.GetBlock(height, charge)
	if err != nil {
		return nil, 0, err
	}
	return br.GetInputs(txIndex, from, max, order, charge)
}

// acceptChain accepts n empty Banff standard blocks at heights 1..n.
func acceptChain(t *testing.T, store *fakeStore, n uint64) []*block.BanffStandardBlock {
	t.Helper()
	blocks := make([]*block.BanffStandardBlock, 0, n)
	parent := ids.GenerateTestID()
	for h := uint64(1); h <= n; h++ {
		blk := newBanffStandard(t, postDurangoTime, parent, h)
		store.accept(blk)
		blocks = append(blocks, blk)
		parent = blk.ID()
	}
	return blocks
}

func TestLastAcceptedHeightUsesHeightIndexOnly(t *testing.T) {
	require := require.New(t)
	store := newFakeStore()
	acceptChain(t, store, 300)
	reader := New(store, nil, testUpgrades)

	// A cold lookup gallops and bisects over the height index: no block is
	// fetched, let alone parsed, and the number of index probes is
	// logarithmic (1,2,4,…,512 then a bisection of at most 9 steps, plus
	// the tip identity check).
	last, err := reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(300), last)
	require.Zero(store.bytesCalls)
	require.LessOrEqual(store.heightCalls, 10+9+1)

	// An unchanged tip is answered from the cached pair with a single call.
	calls, heightCalls := store.calls, store.heightCalls
	last, err = reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(300), last)
	require.Equal(calls+1, store.calls)
	require.Equal(heightCalls, store.heightCalls)

	// A tip that advanced by one costs a handful of probes from the known
	// height, still without touching any block.
	acceptChain(t, store, 301)
	heightCalls = store.heightCalls
	last, err = reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(301), last)
	require.LessOrEqual(store.heightCalls-heightCalls, 4)
	require.Zero(store.bytesCalls)

	// Reads above the tip are rejected through the index as well, and the
	// error names the tip.
	_, err = getBlock(reader, 302, nil)
	require.ErrorIs(err, pchain.ErrNotAccepted)
	require.ErrorContains(err, "last accepted 301")
	require.Zero(store.bytesCalls)
}

func TestLastAcceptedHeightWhenTipMovesDuringLookup(t *testing.T) {
	require := require.New(t)
	store := newFakeStore()
	blocks := acceptChain(t, store, 3)
	reader := New(store, nil, testUpgrades)

	// The tip ID read first is stale (block 2) while the index already holds
	// block 3: the index is authoritative, and the stale pair is not cached.
	store.staleLastAccepted = blocks[1].ID()
	store.staleReads = 1
	last, err := reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(3), last)
	require.Zero(store.staleReads)

	// The next call sees the real tip ID, searches the index again and
	// caches the consistent pair.
	heightCalls := store.heightCalls
	last, err = reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(3), last)
	require.Greater(store.heightCalls, heightCalls)

	heightCalls = store.heightCalls
	last, err = reader.LastAcceptedHeight()
	require.NoError(err)
	require.Equal(uint64(3), last)
	require.Equal(heightCalls, store.heightCalls)
	require.Zero(store.bytesCalls)
}

func TestGetInputsStringOrderIsLazyAndCached(t *testing.T) {
	require := require.New(t)
	f := newInputFixture(t)
	br, err := f.reader.GetBlock(f.height, nil)
	require.NoError(err)
	db, ok := f.reader.blocks.Get(f.height)
	require.True(ok)

	// Canonical reads and empty string-ordered pages never order anything.
	_, total, err := br.GetInputs(0, 0, 3, pchain.InputOrderCanonical, nil)
	require.NoError(err)
	require.Positive(total)
	empty, _, err := br.GetInputs(0, 0, 0, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Empty(empty)
	require.NotNil(empty)
	empty, _, err = br.GetInputs(0, total, 5, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Empty(empty)
	require.Nil(db.byString[0].perm)

	// The first non-empty string-ordered page orders the whole transaction
	// once; later pages, from any block reader, reuse the same permutation.
	page, _, err := br.GetInputs(0, 0, 1, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Len(page, 1)
	perm := db.byString[0].perm
	require.Len(perm, int(total))

	again, err := f.reader.GetBlock(f.height, nil)
	require.NoError(err)
	_, _, err = again.GetInputs(0, 1, 2, pchain.InputOrderByUTXOTxIDString, nil)
	require.NoError(err)
	require.Equal(&perm[0], &db.byString[0].perm[0])
}

// TestOwnerlessOutputResolves covers an output with threshold 0 and no
// addresses, which secp256k1fx permits: it is located, so it resolves, with an
// empty owner list, in both input orders.
func TestOwnerlessOutputResolves(t *testing.T) {
	require := require.New(t)

	ownerless := baseTx(
		[]*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 5, 0)},
		[]*avax.TransferableOutput{transferOutput(10, secp256k1fx.OutputOwners{Threshold: 0})},
	)
	producer := signedTx(t, &ownerless)
	stakingTx := signedTx(t, permissionlessValidator(constants.PrimaryNetworkID, ids.GenerateTestNodeID(), []*avax.TransferableInput{
		transferInput(producer.ID(), 0, 10), // spent without signatures
	}))
	store := newFakeStore()
	store.txs[producer.ID()] = producer
	store.accept(newBanffStandard(t, postDurangoTime, ids.GenerateTestID(), 1, stakingTx))
	reader := New(store, nil, testUpgrades)

	for _, order := range []pchain.InputOrder{pchain.InputOrderCanonical, pchain.InputOrderByUTXOTxIDString} {
		inputs, total, err := getInputs(reader, 1, 0, 0, 1, order, nil)
		require.NoError(err)
		require.Equal(uint32(1), total)
		require.Len(inputs, 1)
		in := inputs[0]
		require.True(in.Resolved)
		require.False(in.Genesis)
		require.Zero(in.Threshold)
		require.Empty(in.Addresses)
		require.Empty(in.SigIndices)
		require.Equal(uint64(10), in.Amount)
	}
}
