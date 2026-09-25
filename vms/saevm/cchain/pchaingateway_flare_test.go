// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package cchain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	ethereum "github.com/ava-labs/libevm"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/graft/coreth/precompile/contracts/pchaingateway"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/engine/snowman/block"
	"github.com/ava-labs/avalanchego/snow/pchain"
	"github.com/ava-labs/avalanchego/upgrade"
	"github.com/ava-labs/avalanchego/upgrade/upgradetest"
	"github.com/ava-labs/avalanchego/vms/saevm/cchain/tx/txtest"
)

var _ pchain.Reader = (*gatewayFakeReader)(nil)

// gatewayFakeReader serves a fixed set of decoded P-chain blocks and inputs.
type gatewayFakeReader struct {
	last   uint64
	blocks map[uint64]*pchain.Block
	inputs map[uint64]map[uint32][]pchain.Input
}

func (r *gatewayFakeReader) LastAcceptedHeight() (uint64, error) {
	return r.last, nil
}

// gatewayFakeBlockBytes is what the fake reader charges per block read.
const gatewayFakeBlockBytes = 1_000

func (r *gatewayFakeReader) GetBlock(height uint64, charge pchain.ByteCharger) (pchain.BlockReader, error) {
	if height > r.last {
		return nil, pchain.ErrNotAccepted
	}
	blk, ok := r.blocks[height]
	if !ok {
		return nil, fmt.Errorf("block %d missing from fake reader", height)
	}
	if charge != nil {
		if err := charge(gatewayFakeBlockBytes); err != nil {
			return nil, err
		}
	}
	return &gatewayFakeBlockReader{blk: blk, inputs: r.inputs[height]}, nil
}

// gatewayFakeBlockReader serves the inputs of one fake block. Inputs carry no
// producing-transaction bytes, so nothing is charged for them.
type gatewayFakeBlockReader struct {
	blk    *pchain.Block
	inputs map[uint32][]pchain.Input
}

func (b *gatewayFakeBlockReader) Block() *pchain.Block {
	return b.blk
}

func (b *gatewayFakeBlockReader) GetInputs(txIndex, from, max uint32, order pchain.InputOrder, _ pchain.ByteCharger) ([]pchain.Input, uint32, error) {
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
	total := uint32(len(ins))
	if from > total {
		return nil, total, pchain.ErrInputIndexOutOfRange
	}
	end := total
	if max < total-from {
		end = from + max
	}
	return ins[from:end], total, nil
}

// gatewayFixture is the P-chain state the fake reader serves: one block at
// height 7 with a single permissionless validator transaction spending one
// resolved input.
type gatewayFixture struct {
	reader   *gatewayFakeReader
	height   uint64
	block    *pchain.Block
	tx       pchain.Tx
	input    pchain.Input
	owner    ids.ShortID
	blockID  ids.ID
	parentID ids.ID
}

func newGatewayFixture() *gatewayFixture {
	const height = 7
	owner := ids.ShortID{0xaa}
	input := pchain.Input{
		UTXOTxID:        ids.ID{0x01},
		UTXOOutputIndex: 3,
		Amount:          1_000,
		Resolved:        true,
		Threshold:       1,
		Addresses:       []ids.ShortID{owner},
		SigIndices:      []uint32{0},
	}
	tx := pchain.Tx{
		Type:             pchain.TxAddPermissionlessValidator,
		Index:            0,
		ID:               ids.ID{0x11},
		NodeID:           ids.NodeID{0x21},
		StartTime:        1_700_000_000,
		EndTime:          1_800_000_000,
		Weight:           1_000,
		DelegationShares: 20_000,
		InputCount:       1,
	}
	blockID, parentID := ids.ID{0x31}, ids.ID{0x30}
	blk := &pchain.Block{
		Height:    height,
		ID:        blockID,
		ParentID:  parentID,
		Timestamp: 1_700_000_000,
		Kind:      pchain.BlockStandard,
		Txs:       []pchain.Tx{tx},
	}
	return &gatewayFixture{
		reader: &gatewayFakeReader{
			last:   height,
			blocks: map[uint64]*pchain.Block{height: blk},
			inputs: map[uint64]map[uint32][]pchain.Input{height: {0: {input}}},
		},
		height:   height,
		block:    blk,
		tx:       tx,
		input:    input,
		owner:    owner,
		blockID:  blockID,
		parentID: parentID,
	}
}

// assertGatewayReads calls every gateway function through eth_call at the
// latest executed block, which must carry committed P-chain height f.height.
func (f *gatewayFixture) assertGatewayReads(ctx context.Context, t *testing.T, sut *SUT) {
	t.Helper()
	require := require.New(t)

	// call takes the (packed, err) pair of a Pack* helper directly.
	call := func(packed []byte, err error) ([]byte, error) {
		require.NoError(err, "packing call data")
		return sut.ethclient.CallContract(ctx, ethereum.CallMsg{
			To:   &pchaingateway.ContractAddress,
			Data: packed,
		}, nil)
	}

	// The precompile account was activated (nonce 1, code 0x01).
	code, err := sut.ethclient.CodeAt(ctx, pchaingateway.ContractAddress, nil)
	require.NoError(err, "%T.CodeAt(gateway)", sut.ethclient)
	require.Equal([]byte{0x01}, code, "gateway account code")

	got, err := call(pchaingateway.PackGetPChainHeight())
	require.NoError(err, "getPChainHeight()")
	height, err := pchaingateway.UnpackGetPChainHeightOutput(got)
	require.NoError(err)
	require.Equal(f.height, height, "getPChainHeight()")

	got, err = call(pchaingateway.PackGetBlock(f.height))
	require.NoError(err, "getBlock(%d)", f.height)
	blk, err := pchaingateway.UnpackGetBlockOutput(got)
	require.NoError(err)
	require.Equal(pchaingateway.GetBlockOutput{
		BlockId:   f.blockID,
		ParentId:  f.parentID,
		Timestamp: f.block.Timestamp,
		Kind:      uint8(pchain.BlockStandard),
		TxCount:   1,
	}, blk, "getBlock(%d)", f.height)

	_, err = call(pchaingateway.PackGetBlock(f.height + 1))
	require.ErrorContains(err, "above committed P-chain height", "getBlock(%d) above the bound", f.height+1)

	got, err = call(pchaingateway.PackGetTransactions(pchaingateway.GetTransactionsInput{
		Height:         f.height,
		FromIndex:      0,
		MaxTxs:         32,
		TypeFilter:     big.NewInt(0),
		MaxInputsPerTx: 8,
	}))
	require.NoError(err, "getTransactions(%d, 0, 32, 0, 8)", f.height)
	txs, err := pchaingateway.UnpackGetTransactionsOutput(got)
	require.NoError(err)
	require.Equal(pchaingateway.GetTransactionsOutput{
		Txs: []pchaingateway.Tx{{
			TxType:           uint8(pchain.TxAddPermissionlessValidator),
			Index:            0,
			TxId:             f.tx.ID,
			NodeId:           f.tx.NodeID,
			StartTime:        f.tx.StartTime,
			EndTime:          f.tx.EndTime,
			Weight:           f.tx.Weight,
			DelegationShares: f.tx.DelegationShares,
			InputCount:       1,
			Inputs: []pchaingateway.Input{{
				UtxoTxId:        f.input.UTXOTxID,
				UtxoOutputIndex: f.input.UTXOOutputIndex,
				Amount:          f.input.Amount,
				Resolved:        true,
				Threshold:       1,
				Addresses:       [][20]byte{f.owner},
				SigIndices:      []uint32{0},
			}},
		}},
		NextIndex: 1,
	}, txs, "getTransactions(%d, 0, 32, 0, 8)", f.height)

	got, err = call(pchaingateway.PackGetStakeOwner(pchaingateway.GetStakeOwnerInput{
		Height:  f.height,
		TxIndex: 0,
		Order:   uint8(pchain.InputOrderCanonical),
	}))
	require.NoError(err, "getStakeOwner(%d, 0, 0)", f.height)
	ownerOut, err := pchaingateway.UnpackGetStakeOwnerOutput(got)
	require.NoError(err)
	require.Equal(pchaingateway.GetStakeOwnerOutput{Owner: f.owner, Resolved: true}, ownerOut, "getStakeOwner(%d, 0, 0)", f.height)
}

// TestPChainGatewayPrecompile drives the gateway end to end: the SAE builder
// commits the block context's P-chain height, execution reads the fake
// P-chain through the snow context, and eth_call exposes the data.
func TestPChainGatewayPrecompile(t *testing.T) {
	t.Run("activated_at_genesis", func(t *testing.T) {
		// The default SUT runs every upgrade, Helicon included, from genesis,
		// so writeState activates the gateway account in the genesis state.
		f := newGatewayFixture()
		key := txtest.NewKey(t)
		ctx, sut := newSUT(t,
			withMaxAllocFor(key.EthAddress()),
			withPChainReader(f.reader),
		)

		code, err := sut.ethclient.CodeAt(ctx, pchaingateway.ContractAddress, big.NewInt(0))
		require.NoError(t, err, "%T.CodeAt(gateway, genesis)", sut.ethclient)
		require.Equal(t, []byte{0x01}, code, "gateway account code at genesis")

		stx := newWallet(key, sut.ctx, sut.Client).newMinimalTx(t)
		require.NoErrorf(t, sut.IssueTx(ctx, stx), "%T.IssueTx()", sut.Client)
		sut.waitForPendingTxs(ctx, t)
		sut.runConsensusLoop(ctx, t, withBlockContext(&block.Context{ProposerPChainHeight: f.height}))

		f.assertGatewayReads(ctx, t, sut)
	})

	t.Run("activated_at_first_helicon_block", func(t *testing.T) {
		// A pre-Helicon genesis carries no gateway account; the first Helicon
		// block activates it in StartExecutingBlock.
		f := newGatewayFixture()
		key := txtest.NewKey(t)
		heliconTime := upgrade.InitiallyActiveTime.Add(5 * time.Second)
		timeOpt, _ := withVMTime(heliconTime)
		ctx, sut := newSUT(t,
			withMaxAllocFor(key.EthAddress()),
			withUpgradeTime(upgradetest.Helicon, heliconTime),
			timeOpt,
			withPChainReader(f.reader),
		)

		code, err := sut.ethclient.CodeAt(ctx, pchaingateway.ContractAddress, big.NewInt(0))
		require.NoError(t, err, "%T.CodeAt(gateway, genesis)", sut.ethclient)
		require.Empty(t, code, "gateway account code before Helicon")

		stx := newWallet(key, sut.ctx, sut.Client).newMinimalTx(t)
		require.NoErrorf(t, sut.IssueTx(ctx, stx), "%T.IssueTx()", sut.Client)
		sut.waitForPendingTxs(ctx, t)
		sut.runConsensusLoop(ctx, t, withBlockContext(&block.Context{ProposerPChainHeight: f.height}))

		f.assertGatewayReads(ctx, t, sut)
	})

	t.Run("no_committed_height_reverts", func(t *testing.T) {
		// A block built without a block context has no committed height.
		f := newGatewayFixture()
		key := txtest.NewKey(t)
		ctx, sut := newSUT(t,
			withMaxAllocFor(key.EthAddress()),
			withPChainReader(f.reader),
		)
		stx := newWallet(key, sut.ctx, sut.Client).newMinimalTx(t)
		require.NoErrorf(t, sut.IssueTx(ctx, stx), "%T.IssueTx()", sut.Client)
		sut.waitForPendingTxs(ctx, t)
		sut.runConsensusLoop(ctx, t)

		packed, err := pchaingateway.PackGetPChainHeight()
		require.NoError(t, err)
		_, err = sut.ethclient.CallContract(ctx, ethereum.CallMsg{
			To:   &pchaingateway.ContractAddress,
			Data: packed,
		}, nil)
		require.ErrorContains(t, err, "P-chain height not committed")
		require.False(t, errors.Is(err, context.DeadlineExceeded))
	})
}
