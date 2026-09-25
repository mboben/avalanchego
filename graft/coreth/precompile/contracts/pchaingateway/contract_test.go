// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchaingateway

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"testing"

	"github.com/ava-labs/libevm/core/vm"
	"github.com/ava-labs/libevm/crypto"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/graft/coreth/precompile/contract"
	"github.com/ava-labs/avalanchego/graft/coreth/precompile/precompiletest"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/pchain"
	"github.com/ava-labs/avalanchego/snow/snowtest"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/formatting/address"
)

// fakeReader is an in-memory pchain.Reader that charges the configured
// stored sizes exactly like the real reader: the block's bytes when it is
// read, then the producing transaction's bytes for each input returned.
type fakeReader struct {
	last   uint64
	blocks map[uint64]*pchain.Block
	// inputs by height and transaction index, in canonical order
	inputs map[uint64]map[uint32][]pchain.Input
	// blockBytes by height; inputBytes by height, transaction index and
	// canonical input position (0 for genesis UTXOs)
	blockBytes map[uint64]uint64
	inputBytes map[uint64]map[uint32][]uint64
	// err, if set, is returned by every method (a node-local failure)
	err error
}

func (f *fakeReader) LastAcceptedHeight() (uint64, error) {
	return f.last, f.err
}

func (f *fakeReader) GetBlock(height uint64, charge pchain.ByteCharger) (pchain.BlockReader, error) {
	if f.err != nil {
		return nil, f.err
	}
	if height > f.last {
		return nil, pchain.ErrNotAccepted
	}
	blk, ok := f.blocks[height]
	if !ok {
		return nil, fmt.Errorf("block %d missing from fake store", height)
	}
	if charge != nil {
		if err := charge(f.blockBytes[height]); err != nil {
			return nil, err
		}
	}
	return &fakeBlockReader{blk: blk, inputs: f.inputs[height], sizes: f.inputBytes[height]}, nil
}

// fakeBlockReader serves the inputs of one fake block, charging each input's
// producing-transaction size like the real reader.
type fakeBlockReader struct {
	blk    *pchain.Block
	inputs map[uint32][]pchain.Input
	sizes  map[uint32][]uint64
}

func (b *fakeBlockReader) Block() *pchain.Block {
	return b.blk
}

func (b *fakeBlockReader) GetInputs(txIndex, from, max uint32, order pchain.InputOrder, charge pchain.ByteCharger) ([]pchain.Input, uint32, error) {
	if order > pchain.InputOrderByUTXOTxIDString {
		return nil, 0, pchain.ErrInvalidInputOrder
	}
	if int(txIndex) >= len(b.blk.Txs) {
		return nil, 0, pchain.ErrTxIndexOutOfRange
	}
	if b.blk.Txs[txIndex].Type == pchain.TxUnsupported {
		return nil, 0, pchain.ErrUnsupportedTx
	}
	ins := b.inputs[txIndex]
	sizes := b.sizes[txIndex]
	perm := make([]int, len(ins))
	for i := range perm {
		perm[i] = i
	}
	if order == pchain.InputOrderByUTXOTxIDString {
		sort.SliceStable(perm, func(a, b int) bool {
			ia, ib := perm[a], perm[b]
			sa, sb := ins[ia].UTXOTxID.String(), ins[ib].UTXOTxID.String()
			if sa != sb {
				return sa < sb
			}
			return ins[ia].UTXOOutputIndex < ins[ib].UTXOOutputIndex
		})
	}
	total := uint32(len(ins))
	if from > total {
		return nil, total, pchain.ErrInputIndexOutOfRange
	}
	end := total
	if max < total-from {
		end = from + max
	}
	page := make([]pchain.Input, 0, end-from)
	for _, i := range perm[from:end] {
		if charge != nil && sizes[i] > 0 { // genesis UTXOs charge nothing
			if err := charge(sizes[i]); err != nil {
				return nil, total, err
			}
		}
		page = append(page, ins[i])
	}
	return page, total, nil
}

// fakeEnv records execution invalidation.
type fakeEnv struct {
	vm.PrecompileEnvironment
	invalidated error
}

func (e *fakeEnv) InvalidateExecution(err error) {
	e.invalidated = err
}

const (
	bound       = uint64(10)
	blockHeight = uint64(10)
	genesisH    = uint64(0)
)

var (
	// byteFirst sorts before stringFirst by bytes but after it by CB58 string.
	byteFirst   = ids.ID{0x01}
	stringFirst = ids.ID{0x03}

	addrA = ids.ShortID{0xa}
	addrB = ids.ShortID{0xb}
	addrC = ids.ShortID{0xc}
	addrD = ids.ShortID{0xd}

	validatorTxID   = ids.ID{0x11}
	delegatorTxID   = ids.ID{0x12}
	importTxID      = ids.ID{0x13}
	autoRenewedTxID = ids.ID{0x14}
	rewardTxID      = ids.ID{0x15}
	genesisTxID     = ids.ID{0x16}
	unresolvedTxID  = ids.ID{0x17}
	nodeID          = ids.NodeID{0x21}
	blockID         = ids.ID{0x31}
	parentID        = ids.ID{0x30}
)

var testInputs = map[uint32][]pchain.Input{
	0: { // validator: canonical order byteFirst, stringFirst
		{UTXOTxID: byteFirst, UTXOOutputIndex: 0, Amount: 700, Resolved: true, Threshold: 2, Addresses: []ids.ShortID{addrA, addrB, addrC}, SigIndices: []uint32{0, 2}},
		{UTXOTxID: stringFirst, UTXOOutputIndex: 1, Amount: 300, Resolved: true, Threshold: 1, Addresses: []ids.ShortID{addrD}, SigIndices: []uint32{0}},
	},
	2: { // delegator: single owner
		{UTXOTxID: byteFirst, UTXOOutputIndex: 5, Amount: 500, Resolved: true, Threshold: 1, Addresses: []ids.ShortID{addrB}, SigIndices: []uint32{0}},
	},
	3: { // auto-renewed: genesis input
		{UTXOTxID: ids.Empty, UTXOOutputIndex: 2, Amount: 1000, Resolved: true, Genesis: true, Threshold: 1, Addresses: []ids.ShortID{addrA}, SigIndices: []uint32{0}},
	},
	5: { // legacy validator: unresolved input
		{UTXOTxID: unresolvedTxID, UTXOOutputIndex: 0, Amount: 2000, SigIndices: []uint32{0}},
	},
}

var testBlock = &pchain.Block{
	Height:    blockHeight,
	ID:        blockID,
	ParentID:  parentID,
	Timestamp: 1_700_000_000,
	Kind:      pchain.BlockStandard,
	Txs: []pchain.Tx{
		{Type: pchain.TxAddPermissionlessValidator, Index: 0, ID: validatorTxID, NodeID: nodeID, StartTime: 1_700_000_000, EndTime: 1_800_000_000, Weight: 1000, DelegationShares: 20_000, InputCount: 2},
		{Type: pchain.TxUnsupported, Index: 1, ID: importTxID},
		{Type: pchain.TxAddPermissionlessDelegator, Index: 2, ID: delegatorTxID, NodeID: nodeID, StartTime: 1_700_000_000, EndTime: 1_750_000_000, Weight: 500, InputCount: 1},
		{Type: pchain.TxAddAutoRenewedValidator, Index: 3, ID: autoRenewedTxID, NodeID: nodeID, StartTime: 1_700_000_000, EndTime: 1_700_000_000 + 3600, Period: 3600, Weight: 1000, DelegationShares: 30_000, AutoCompoundRewardShares: 5, InputCount: 1},
		{Type: pchain.TxRewardAutoRenewedValidator, Index: 4, ID: rewardTxID, StartTime: 1_700_000_000, EndTime: 1_700_000_000, StakerTxID: autoRenewedTxID},
		{Type: pchain.TxAddValidator, Index: 5, ID: genesisTxID, NodeID: nodeID, StartTime: 5, EndTime: 6, Weight: 2000, InputCount: 1},
	},
}

// Stored sizes charged by the fake reader.
const (
	testBlockBytes    uint64 = 4_000
	genesisBlockBytes uint64 = 100
)

// testInputBytes are the producing-tx sizes of testInputs, by tx index and
// canonical input position.
var testInputBytes = map[uint32][]uint64{
	0: {300, 400},
	2: {200},
	3: {0}, // genesis UTXO: no producing transaction
	5: {700},
}

// decodedBytes is the block size plus the producing-tx sizes of the given
// canonical input positions of tx txIndex.
func decodedBytes(txIndex uint32, inputs ...int) uint64 {
	total := testBlockBytes
	for _, i := range inputs {
		total += testInputBytes[txIndex][i]
	}
	return total
}

// ownerlessInput is a located output with threshold 0: it has no owner
// addresses and can be spent without signatures (secp256k1fx permits it).
var ownerlessInput = pchain.Input{UTXOTxID: byteFirst, UTXOOutputIndex: 9, Amount: 500, Resolved: true}

// newOwnerlessReader serves the fixture block with the delegator (tx 2)
// funded by ownerlessInput instead of its usual single-owner input.
func newOwnerlessReader() *fakeReader {
	r := newFakeReader()
	r.inputs = map[uint64]map[uint32][]pchain.Input{blockHeight: {2: {ownerlessInput}}}
	r.inputBytes = map[uint64]map[uint32][]uint64{blockHeight: {2: {testInputBytes[2][0]}}}
	return r
}

func newFakeReader() *fakeReader {
	return &fakeReader{
		last: bound,
		blocks: map[uint64]*pchain.Block{
			blockHeight: testBlock,
			genesisH:    {Height: 0, ID: parentID, Kind: pchain.BlockStandard},
		},
		inputs:     map[uint64]map[uint32][]pchain.Input{blockHeight: testInputs},
		blockBytes: map[uint64]uint64{blockHeight: testBlockBytes, genesisH: genesisBlockBytes},
		inputBytes: map[uint64]map[uint32][]uint64{blockHeight: testInputBytes},
	}
}

func snowContextWithReader(t testing.TB, reader pchain.Reader) *snow.Context {
	ctx := snowtest.Context(t, snowtest.CChainID)
	ctx.PChainReader = reader
	return ctx
}

func withBound(height uint64) func(*contract.MockBlockContext) {
	return func(mbc *contract.MockBlockContext) {
		mbc.EXPECT().PChainHeight().Return(height, true).AnyTimes()
	}
}

func withoutBound(mbc *contract.MockBlockContext) {
	mbc.EXPECT().PChainHeight().Return(uint64(0), false).AnyTimes()
}

// mustPack returns a function that fails the test on a packing error, so that
// multi-value Pack* results can be used inline: mustPack(t)(PackGetBlock(1)).
func mustPack(t testing.TB) func(packed []byte, err error) []byte {
	return func(packed []byte, err error) []byte {
		t.Helper()
		require.NoError(t, err)
		return packed
	}
}

// stripSelector removes the 4-byte function selector from a packed call.
func stripSelector(packed []byte) []byte {
	return packed[contract.SelectorLen:]
}

func expectedTx(t testing.TB, index uint32, maxInputs uint32) Tx {
	t.Helper()
	tx := testBlock.Txs[index]
	ins := testInputs[index]
	if uint32(len(ins)) > maxInputs {
		ins = ins[:maxInputs]
	}
	return toTx(tx, ins)
}

func TestABISelectorsAndSignatures(t *testing.T) {
	require := require.New(t)
	want := map[string]string{
		"getPChainHeight":      "getPChainHeight()",
		"getBlock":             "getBlock(uint64)",
		"getTransactions":      "getTransactions(uint64,uint32,uint32,uint256,uint32)",
		"getTransactionInputs": "getTransactionInputs(uint64,uint32,uint32,uint32,uint8)",
		"getStakeOwner":        "getStakeOwner(uint64,uint32,uint8)",
	}
	require.Len(PChainGatewayABI.Methods, len(want))
	for name, sig := range want {
		method, ok := PChainGatewayABI.Methods[name]
		require.True(ok, name)
		require.Equal(sig, method.Sig)
		require.Equal(crypto.Keccak256([]byte(sig))[:4], method.ID)
	}
}

func TestPackUnpackRoundTrips(t *testing.T) {
	require := require.New(t)

	// Nested dynamic tuples must survive a round trip through the hand-written ABI.
	txsOut := GetTransactionsOutput{
		Txs:       []Tx{expectedTx(t, 0, 8), expectedTx(t, 3, 8), toTx(testBlock.Txs[4], nil)},
		NextIndex: 5,
	}
	packed, err := PackGetTransactionsOutput(txsOut)
	require.NoError(err)
	unpacked, err := UnpackGetTransactionsOutput(packed)
	require.NoError(err)
	require.Equal(txsOut, unpacked)

	inputsOut := GetTransactionInputsOutput{Inputs: []Input{toInput(testInputs[0][0]), toInput(testInputs[5][0])}, InputCount: 7}
	packed, err = PackGetTransactionInputsOutput(inputsOut)
	require.NoError(err)
	unpackedInputs, err := UnpackGetTransactionInputsOutput(packed)
	require.NoError(err)
	require.Equal(inputsOut, unpackedInputs)

	blockOut := GetBlockOutput{BlockId: blockID, ParentId: parentID, Timestamp: 7, Kind: 1, TxCount: 9}
	packed, err = PackGetBlockOutput(blockOut)
	require.NoError(err)
	unpackedBlock, err := UnpackGetBlockOutput(packed)
	require.NoError(err)
	require.Equal(blockOut, unpackedBlock)

	ownerOut := GetStakeOwnerOutput{Owner: addrA, Resolved: true}
	packed, err = PackGetStakeOwnerOutput(ownerOut)
	require.NoError(err)
	unpackedOwner, err := UnpackGetStakeOwnerOutput(packed)
	require.NoError(err)
	require.Equal(ownerOut, unpackedOwner)

	in := GetTransactionsInput{Height: 1, FromIndex: 2, MaxTxs: 3, TypeFilter: big.NewInt(4), MaxInputsPerTx: 5}
	packed, err = PackGetTransactions(in)
	require.NoError(err)
	unpackedIn, err := UnpackGetTransactionsInput(stripSelector(packed))
	require.NoError(err)
	require.Equal(in, unpackedIn)

	height, err := UnpackGetPChainHeightOutput(mustPack(t)(PackGetPChainHeightOutput(42)))
	require.NoError(err)
	require.Equal(uint64(42), height)
}

func TestGetPChainHeight(t *testing.T) {
	tests := []precompiletest.PrecompileTest{
		{
			Name:              "returns committed height",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetPChainHeight()) },
			SetupBlockContext: withBound(bound),
			SuppliedGas:       GetPChainHeightGas,
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetPChainHeightOutput(bound)),
		},
		{
			Name:              "reverts without committed height",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetPChainHeight()) },
			SetupBlockContext: withoutBound,
			SuppliedGas:       GetPChainHeightGas,
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonHeightNotCommitted),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "out of gas",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetPChainHeight()) },
			SetupBlockContext: withBound(bound),
			SuppliedGas:       GetPChainHeightGas - 1,
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
	}
	precompiletest.RunPrecompileTests(t, Module, tests)
}

func TestGetBlock(t *testing.T) {
	reader := newFakeReader()
	snowCtx := snowContextWithReader(t, reader)
	env := &fakeEnv{}

	tests := []precompiletest.PrecompileTest{
		{
			Name:              "block at bound",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(blockHeight)) },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(testBlockBytes),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetBlockOutput(GetBlockOutput{
				BlockId: blockID, ParentId: parentID, Timestamp: 1_700_000_000, Kind: uint8(pchain.BlockStandard), TxCount: 6,
			})),
		},
		{
			Name:              "genesis block",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(0)) },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(genesisBlockBytes),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetBlockOutput(GetBlockOutput{BlockId: parentID})),
		},
		{
			Name:              "insufficient gas for the block bytes",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(blockHeight)) },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(testBlockBytes) - 1,
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
		{
			Name:              "height above bound reverts",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(bound + 1)) },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonHeightAboveBound),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "no committed height reverts",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(1)) },
			SetupBlockContext: withoutBound,
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonHeightNotCommitted),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "malformed input reverts",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(1))[:6] },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetBlockGas(0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonInvalidInput),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "height not accepted locally invalidates execution",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(bound)) },
			SetupBlockContext: withBound(bound + 5), // header bound ahead of the lagging local P-chain
			SnowContext: snowContextWithReader(t, &fakeReader{
				last:   bound - 1,
				blocks: map[uint64]*pchain.Block{},
			}),
			PrecompileEnv: env,
			SuppliedGas:   GetBlockGas(0),
			ReadOnly:      true,
			ExpectedErr:   pchain.ErrNotAccepted,
			AfterHook: func(t testing.TB, _ contract.StateDB) {
				require.ErrorIs(t, env.invalidated, pchain.ErrNotAccepted)
			},
		},
		{
			Name:              "missing reader invalidates execution",
			InputFn:           func(t testing.TB) []byte { return mustPack(t)(PackGetBlock(1)) },
			SetupBlockContext: withBound(bound),
			SnowContext:       snowtest.Context(t, snowtest.CChainID),
			PrecompileEnv:     env,
			SuppliedGas:       GetBlockGas(0),
			ReadOnly:          true,
			ExpectedErr:       errPChainReaderUnavailable,
		},
	}
	precompiletest.RunPrecompileTests(t, Module, tests)
}

func TestGetTransactions(t *testing.T) {
	reader := newFakeReader()
	snowCtx := snowContextWithReader(t, reader)

	call := func(in GetTransactionsInput) func(testing.TB) []byte {
		return func(t testing.TB) []byte { return mustPack(t)(PackGetTransactions(in)) }
	}
	allTypes := big.NewInt(0)
	validatorsOnly := new(big.Int).SetBit(big.NewInt(0), int(pchain.TxAddPermissionlessValidator), 1)
	autoRenew := new(big.Int).SetBit(big.NewInt(0), int(pchain.TxAddAutoRenewedValidator), 1)
	autoRenew.SetBit(autoRenew, int(pchain.TxRewardAutoRenewedValidator), 1)

	tests := []precompiletest.PrecompileTest{
		{
			Name:              "whole block, all types, inline inputs",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 32, TypeFilter: allTypes, MaxInputsPerTx: 8}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(32, 8, decodedBytes(0, 0, 1)+testInputBytes[2][0]+testInputBytes[3][0]+testInputBytes[5][0], 3+1+1+1), // all inline inputs
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionsOutput(GetTransactionsOutput{
				Txs:       []Tx{expectedTx(t, 0, 8), expectedTx(t, 2, 8), expectedTx(t, 3, 8), toTx(testBlock.Txs[4], nil), expectedTx(t, 5, 8)},
				NextIndex: 6,
			})),
		},
		{
			Name:              "type filter",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 32, TypeFilter: autoRenew, MaxInputsPerTx: 0}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(32, 0, testBlockBytes, 0),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionsOutput(GetTransactionsOutput{
				Txs:       []Tx{toTx(testBlock.Txs[3], nil), toTx(testBlock.Txs[4], nil)},
				NextIndex: 6,
			})),
		},
		{
			Name:              "pagination stops after maxTxs and reports nextIndex",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 2, TypeFilter: allTypes, MaxInputsPerTx: 1}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(2, 1, decodedBytes(0, 0)+testInputBytes[2][0], 3+1),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionsOutput(GetTransactionsOutput{
				Txs:       []Tx{expectedTx(t, 0, 1), expectedTx(t, 2, 1)},
				NextIndex: 3,
			})),
		},
		{
			Name:              "resume from nextIndex",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 3, MaxTxs: 2, TypeFilter: validatorsOnly, MaxInputsPerTx: 8}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(2, 8, testBlockBytes, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetTransactionsOutput(GetTransactionsOutput{Txs: []Tx{}, NextIndex: 6})),
		},
		{
			Name:              "fromIndex equal to txCount is an empty page",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 6, MaxTxs: 1, TypeFilter: allTypes, MaxInputsPerTx: 0}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(1, 0, testBlockBytes, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetTransactionsOutput(GetTransactionsOutput{Txs: []Tx{}, NextIndex: 6})),
		},
		{
			Name:              "fromIndex above txCount reverts",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 7, MaxTxs: 1, TypeFilter: allTypes, MaxInputsPerTx: 0}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(1, 0, testBlockBytes, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonTxIndexOutOfRange),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "insufficient gas for a producing transaction",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 1, TypeFilter: allTypes, MaxInputsPerTx: 8}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(1, 8, decodedBytes(0, 0, 1), 0) - 1, // one byte short of the second input's tx
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
		{
			Name:              "maxTxs above cap reverts",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: MaxTxsPerCall + 1, TypeFilter: allTypes, MaxInputsPerTx: 0}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       BlockReadGas,
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonPageSizeExceedsCap),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "maxInputsPerTx above cap reverts",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 1, TypeFilter: allTypes, MaxInputsPerTx: MaxInlineInputsPerTx + 1}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       BlockReadGas,
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonPageSizeExceedsCap),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "height above bound reverts after charging the page",
			InputFn:           call(GetTransactionsInput{Height: bound + 1, FromIndex: 0, MaxTxs: 4, TypeFilter: allTypes, MaxInputsPerTx: 2}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(4, 2, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonHeightAboveBound),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "insufficient gas for the page",
			InputFn:           call(GetTransactionsInput{Height: blockHeight, FromIndex: 0, MaxTxs: 4, TypeFilter: allTypes, MaxInputsPerTx: 2}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionsGas(4, 2, 0, 0) - 1,
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
	}
	precompiletest.RunPrecompileTests(t, Module, tests)
}

func TestGetTransactionInputs(t *testing.T) {
	reader := newFakeReader()
	snowCtx := snowContextWithReader(t, reader)

	call := func(in GetTransactionInputsInput) func(testing.TB) []byte {
		return func(t testing.TB) []byte { return mustPack(t)(PackGetTransactionInputs(in)) }
	}
	canonical := uint8(pchain.InputOrderCanonical)
	byString := uint8(pchain.InputOrderByUTXOTxIDString)

	tests := []precompiletest.PrecompileTest{
		{
			Name:              "canonical order",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: 64, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(64, decodedBytes(0, 0, 1), 0, 4),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(testInputs[0][0]), toInput(testInputs[0][1])},
				InputCount: 2,
			})),
		},
		{
			Name:              "order by UTXO tx id string",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: 64, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(64, decodedBytes(0, 1, 0), 2, 4), // orders both inputs of tx 0
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(testInputs[0][1]), toInput(testInputs[0][0])},
				InputCount: 2,
			})),
		},
		{
			Name:              "paging",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 1, MaxInputs: 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, decodedBytes(0, 1), 0, 1),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(testInputs[0][1])},
				InputCount: 2,
			})),
		},
		{
			Name:              "unresolved input is returned as such",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 5, FromInput: 0, MaxInputs: 8, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(8, decodedBytes(5, 0), 0, 0),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(testInputs[5][0])},
				InputCount: 1,
			})),
		},
		{
			Name:              "invalid order reverts",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: 1, Order: 2}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       BlockReadGas,
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonInvalidInputOrder),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "maxInputs above cap reverts",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: MaxInputsPerCall + 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       BlockReadGas,
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonPageSizeExceedsCap),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "empty page in string order orders nothing",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: 0, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(0, testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{Inputs: []Input{}, InputCount: 2})),
		},
		{
			Name:              "page past the last input in string order orders nothing",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 2, MaxInputs: 5, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(5, testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{Inputs: []Input{}, InputCount: 2})),
		},
		{
			Name:              "one input in string order pays for ordering the whole transaction",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 1, MaxInputs: 1, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, decodedBytes(0, 0), 2, 3), // second by string is the byteFirst input
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(testInputs[0][0])},
				InputCount: 2,
			})),
		},
		{
			Name:              "insufficient gas for ordering",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 0, MaxInputs: 1, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, testBlockBytes, 2, 0) - 1, // charged before any input is resolved
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
		{
			Name:              "ownerless output resolves with no addresses in canonical order",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 2, FromInput: 0, MaxInputs: 8, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowContextWithReader(t, newOwnerlessReader()),
			SuppliedGas:       GetTransactionInputsGas(8, decodedBytes(2, 0), 0, 0),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(ownerlessInput)},
				InputCount: 1,
			})),
		},
		{
			Name:              "ownerless output resolves with no addresses in string order",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 2, FromInput: 0, MaxInputs: 8, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowContextWithReader(t, newOwnerlessReader()),
			SuppliedGas:       GetTransactionInputsGas(8, decodedBytes(2, 0), 1, 0),
			ReadOnly:          true,
			ExpectedRes: mustPack(t)(PackGetTransactionInputsOutput(GetTransactionInputsOutput{
				Inputs:     []Input{toInput(ownerlessInput)},
				InputCount: 1,
			})),
		},
		{
			Name:              "unsupported tx reverts",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 1, FromInput: 0, MaxInputs: 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonUnsupportedTx),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "tx index out of range reverts",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 6, FromInput: 0, MaxInputs: 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonTxIndexOutOfRange),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "input index out of range reverts",
			InputFn:           call(GetTransactionInputsInput{Height: blockHeight, TxIndex: 0, FromInput: 3, MaxInputs: 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetTransactionInputsGas(1, testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonInputIndexOutOfRange),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
	}
	precompiletest.RunPrecompileTests(t, Module, tests)
}

func TestGetStakeOwner(t *testing.T) {
	require := require.New(t)
	reader := newFakeReader()
	snowCtx := snowContextWithReader(t, reader)
	hrp := constants.GetHRP(snowCtx.NetworkID)

	// The validator's first canonical input is owned by A, B, C with A and C
	// signing; the indexer picks the lexically smallest bech32 string of the
	// signers.
	bech := func(addr ids.ShortID) string {
		str, err := address.FormatBech32(hrp, addr.Bytes())
		require.NoError(err)
		return str
	}
	canonicalOwner := addrA
	if bech(addrC) < bech(addrA) {
		canonicalOwner = addrC
	}
	require.Negative(bytes.Compare(byteFirst[:], stringFirst[:]))
	require.Greater(byteFirst.String(), stringFirst.String())

	call := func(in GetStakeOwnerInput) func(testing.TB) []byte {
		return func(t testing.TB) []byte { return mustPack(t)(PackGetStakeOwner(in)) }
	}
	canonical := uint8(pchain.InputOrderCanonical)
	byString := uint8(pchain.InputOrderByUTXOTxIDString)
	env := &fakeEnv{}

	tests := []precompiletest.PrecompileTest{
		{
			Name:              "canonical order picks the smallest bech32 signer of the first input",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 0, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(0, 0), 0, 2),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{Owner: canonicalOwner, Resolved: true})),
		},
		{
			Name:              "string order picks the other input",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 0, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(0, 1), 2, 1), // orders both inputs of tx 0
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{Owner: addrD, Resolved: true})),
		},
		{
			Name:              "single owner",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 2, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(2, 0), 0, 1),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{Owner: addrB, Resolved: true})),
		},
		{
			Name:              "genesis input has no owner",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 3, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(3, 0), 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "no inputs has no owner",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 4, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "unresolved input has no owner",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 5, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(5, 0), 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "ownerless output has no owner in canonical order",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 2, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowContextWithReader(t, newOwnerlessReader()),
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(2, 0), 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "ownerless output has no owner in string order",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 2, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowContextWithReader(t, newOwnerlessReader()),
			SuppliedGas:       GetStakeOwnerGas(decodedBytes(2, 0), 1, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "insufficient gas for ordering",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 0, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(testBlockBytes, 2, 0) - 1,
			ReadOnly:          true,
			ExpectedErr:       vm.ErrOutOfGas,
		},
		{
			Name:              "no inputs in string order orders nothing",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 4, Order: byString}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       mustPack(t)(PackGetStakeOwnerOutput(GetStakeOwnerOutput{})),
		},
		{
			Name:              "invalid order reverts",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 0, Order: 7}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(0, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonInvalidInputOrder),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "unsupported tx reverts",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 1, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowCtx,
			SuppliedGas:       GetStakeOwnerGas(testBlockBytes, 0, 0),
			ReadOnly:          true,
			ExpectedRes:       revertReason(reasonUnsupportedTx),
			ExpectedErr:       vm.ErrExecutionReverted,
		},
		{
			Name:              "reader failure invalidates execution",
			InputFn:           call(GetStakeOwnerInput{Height: blockHeight, TxIndex: 0, Order: canonical}),
			SetupBlockContext: withBound(bound),
			SnowContext:       snowContextWithReader(t, &fakeReader{err: errDatabaseCorrupted}),
			PrecompileEnv:     env,
			SuppliedGas:       GetStakeOwnerGas(0, 0, 0),
			ReadOnly:          true,
			ExpectedErr:       errDatabaseCorrupted,
			AfterHook: func(testing.TB, contract.StateDB) {
				require.ErrorIs(env.invalidated, errDatabaseCorrupted)
			},
		},
	}
	precompiletest.RunPrecompileTests(t, Module, tests)
}

// errDatabaseCorrupted stands in for a node-local reader failure.
var errDatabaseCorrupted = errors.New("database corrupted")

func TestStakeOwnerCandidates(t *testing.T) {
	require := require.New(t)

	in := pchain.Input{Addresses: []ids.ShortID{addrA, addrB, addrC}, SigIndices: []uint32{1}}
	require.Equal([]ids.ShortID{addrB}, stakeOwnerCandidates(in))

	// Without signature indices every owner is a candidate.
	in.SigIndices = nil
	require.Equal([]ids.ShortID{addrA, addrB, addrC}, stakeOwnerCandidates(in))

	// Out-of-range indices fall back to every owner.
	in.SigIndices = []uint32{9}
	require.Equal([]ids.ShortID{addrA, addrB, addrC}, stakeOwnerCandidates(in))

	// A single owner needs no signatures to be attributed.
	require.Equal([]ids.ShortID{addrD}, stakeOwnerCandidates(pchain.Input{Addresses: []ids.ShortID{addrD}}))
}

// TestSmallestBech32PrefersStringOrder mirrors the indexer's
// TestOwnerOrderIsByAddressBytesNotBech32: byte order and bech32 string order
// disagree, and the attribution follows the string order.
func TestSmallestBech32PrefersStringOrder(t *testing.T) {
	require := require.New(t)
	const hrp = "costwo"

	var first, second ids.ShortID
	found := false
	for i := 0; i < 10_000 && !found; i++ {
		a, b := ids.GenerateTestShortID(), ids.GenerateTestShortID()
		strA, err := address.FormatBech32(hrp, a.Bytes())
		require.NoError(err)
		strB, err := address.FormatBech32(hrp, b.Bytes())
		require.NoError(err)
		switch {
		case bytes.Compare(a.Bytes(), b.Bytes()) < 0 && strA > strB:
			first, second, found = a, b, true // first is smaller by bytes, larger by string
		case bytes.Compare(b.Bytes(), a.Bytes()) < 0 && strB > strA:
			first, second, found = b, a, true
		}
	}
	require.True(found, "could not find a pair whose byte and bech32 orderings disagree")

	owner, err := smallestBech32(hrp, []ids.ShortID{first, second})
	require.NoError(err)
	require.Equal(second, owner)
}
