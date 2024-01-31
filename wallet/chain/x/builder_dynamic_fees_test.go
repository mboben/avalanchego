// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package x

import (
	stdcontext "context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/avm/txs/fees"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/components/verify"
	"github.com/ava-labs/avalanchego/vms/nftfx"
	"github.com/ava-labs/avalanchego/vms/platformvm/stakeable"
	"github.com/ava-labs/avalanchego/vms/propertyfx"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/chain/x/mocks"

	commonfees "github.com/ava-labs/avalanchego/vms/components/fees"
)

var (
	testKeys     = secp256k1.TestKeys()
	testUnitFees = commonfees.Dimensions{
		1 * units.MicroAvax,
		2 * units.MicroAvax,
		3 * units.MicroAvax,
		4 * units.MicroAvax,
	}
	testBlockMaxConsumedUnits = commonfees.Dimensions{
		math.MaxUint64,
		math.MaxUint64,
		math.MaxUint64,
		math.MaxUint64,
	}
)

// These tests create and sign a tx, then verify that utxos included
// in the tx are exactly necessary to pay fees for it

func TestBaseTx(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	be := mocks.NewMockBuilderBackend(ctrl)

	var (
		utxosKey           = testKeys[1]
		utxoAddr           = utxosKey.PublicKey().Address()
		utxos, avaxAssetID = testUTXOsList(utxosKey)

		outputsToMove = []*avax.TransferableOutput{{
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 7 * units.Avax,
				OutputOwners: secp256k1fx.OutputOwners{
					Threshold: 1,
					Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
				},
			},
		}}
	)

	b := &DynamicFeesBuilder{
		addrs:   set.Of(utxoAddr),
		backend: be,
	}

	be.EXPECT().AVAXAssetID().Return(avaxAssetID).AnyTimes()
	be.EXPECT().NetworkID().Return(constants.MainnetID).AnyTimes()
	be.EXPECT().BlockchainID().Return(constants.PlatformChainID)
	be.EXPECT().UTXOs(gomock.Any(), constants.PlatformChainID).Return(utxos, nil)

	utx, err := b.NewBaseTx(
		outputsToMove,
		testUnitFees,
		testBlockMaxConsumedUnits,
	)
	require.NoError(err)

	var (
		kc  = secp256k1fx.NewKeychain(utxosKey)
		sbe = mocks.NewMockSignerBackend(ctrl)
		s   = NewSigner(kc, sbe)
	)

	for _, utxo := range utxos {
		sbe.EXPECT().GetUTXO(gomock.Any(), gomock.Any(), utxo.InputID()).Return(utxo, nil).AnyTimes()
	}

	tx, err := s.SignUnsigned(stdcontext.Background(), utx)
	require.NoError(err)

	fc := &fees.Calculator{
		IsEForkActive:    true,
		Codec:            Parser.Codec(),
		FeeManager:       commonfees.NewManager(testUnitFees),
		ConsumedUnitsCap: testBlockMaxConsumedUnits,
		Credentials:      tx.Creds,
	}
	require.NoError(utx.Visit(fc))
	require.Equal(5930*units.MicroAvax, fc.Fee)

	ins := utx.Ins
	outs := utx.Outs
	require.Len(ins, 2)
	require.Len(outs, 2)
	require.Equal(fc.Fee+outputsToMove[0].Out.Amount(), ins[0].In.Amount()+ins[1].In.Amount()-outs[0].Out.Amount())
	require.Equal(outputsToMove[0], outs[1])
}

func TestCreateAssetTx(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	be := mocks.NewMockBuilderBackend(ctrl)

	var (
		utxosKey           = testKeys[1]
		utxoAddr           = utxosKey.PublicKey().Address()
		utxos, avaxAssetID = testUTXOsList(utxosKey)

		assetName          = "Team Rocket"
		symbol             = "TR"
		denomination uint8 = 0
		initialState       = map[uint32][]verify.State{
			0: {
				&secp256k1fx.MintOutput{
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[0].PublicKey().Address()},
					},
				}, &secp256k1fx.MintOutput{
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[0].PublicKey().Address()},
					},
				},
			},
			1: {
				&nftfx.MintOutput{
					GroupID: 1,
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[1].PublicKey().Address()},
					},
				},
				&nftfx.MintOutput{
					GroupID: 2,
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[1].PublicKey().Address()},
					},
				},
			},
			2: {
				&propertyfx.MintOutput{
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[2].PublicKey().Address()},
					},
				},
				&propertyfx.MintOutput{
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{testKeys[2].PublicKey().Address()},
					},
				},
			},
		}
	)

	b := &DynamicFeesBuilder{
		addrs:   set.Of(utxoAddr),
		backend: be,
	}

	be.EXPECT().AVAXAssetID().Return(avaxAssetID).AnyTimes()
	be.EXPECT().NetworkID().Return(constants.MainnetID).AnyTimes()
	be.EXPECT().BlockchainID().Return(constants.PlatformChainID).AnyTimes()
	be.EXPECT().UTXOs(gomock.Any(), constants.PlatformChainID).Return(utxos, nil)

	utx, err := b.NewCreateAssetTx(
		assetName,
		symbol,
		denomination,
		initialState,
		testUnitFees,
		testBlockMaxConsumedUnits,
	)
	require.NoError(err)

	var (
		kc  = secp256k1fx.NewKeychain(utxosKey)
		sbe = mocks.NewMockSignerBackend(ctrl)
		s   = NewSigner(kc, sbe)
	)

	for _, utxo := range utxos {
		sbe.EXPECT().GetUTXO(gomock.Any(), gomock.Any(), utxo.InputID()).Return(utxo, nil).AnyTimes()
	}

	tx, err := s.SignUnsigned(stdcontext.Background(), utx)
	require.NoError(err)

	fc := &fees.Calculator{
		IsEForkActive:    true,
		Codec:            Parser.Codec(),
		FeeManager:       commonfees.NewManager(testUnitFees),
		ConsumedUnitsCap: testBlockMaxConsumedUnits,
		Credentials:      tx.Creds,
	}
	require.NoError(utx.Visit(fc))
	require.Equal(5898*units.MicroAvax, fc.Fee)

	ins := utx.Ins
	outs := utx.Outs
	require.Len(ins, 2)
	require.Len(outs, 1)
	require.Equal(fc.Fee, ins[0].In.Amount()+ins[1].In.Amount()-outs[0].Out.Amount())
}

func TestImportTx(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	be := mocks.NewMockBuilderBackend(ctrl)

	var (
		utxosKey           = testKeys[1]
		utxoAddr           = utxosKey.PublicKey().Address()
		sourceChainID      = ids.GenerateTestID()
		utxos, avaxAssetID = testUTXOsList(utxosKey)

		importKey = testKeys[0]
		importTo  = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs: []ids.ShortID{
				importKey.Address(),
			},
		}
	)

	importedUtxo := utxos[0]
	utxos = utxos[1:]

	b := &DynamicFeesBuilder{
		addrs:   set.Of(utxoAddr),
		backend: be,
	}
	be.EXPECT().AVAXAssetID().Return(avaxAssetID).AnyTimes()
	be.EXPECT().NetworkID().Return(constants.MainnetID).AnyTimes()
	be.EXPECT().BlockchainID().Return(constants.PlatformChainID).AnyTimes()
	be.EXPECT().UTXOs(gomock.Any(), sourceChainID).Return([]*avax.UTXO{importedUtxo}, nil)
	be.EXPECT().UTXOs(gomock.Any(), constants.PlatformChainID).Return(utxos, nil)

	utx, err := b.NewImportTx(
		sourceChainID,
		importTo,
		testUnitFees,
		testBlockMaxConsumedUnits,
	)
	require.NoError(err)

	var (
		kc  = secp256k1fx.NewKeychain(utxosKey)
		sbe = mocks.NewMockSignerBackend(ctrl)
		s   = NewSigner(kc, sbe)
	)

	sbe.EXPECT().GetUTXO(gomock.Any(), gomock.Any(), importedUtxo.InputID()).Return(importedUtxo, nil).AnyTimes()
	for _, utxo := range utxos {
		sbe.EXPECT().GetUTXO(gomock.Any(), gomock.Any(), utxo.InputID()).Return(utxo, nil).AnyTimes()
	}

	tx, err := s.SignUnsigned(stdcontext.Background(), utx)
	require.NoError(err)

	fc := &fees.Calculator{
		IsEForkActive:    true,
		Codec:            Parser.Codec(),
		FeeManager:       commonfees.NewManager(testUnitFees),
		ConsumedUnitsCap: testBlockMaxConsumedUnits,
		Credentials:      tx.Creds,
	}
	require.NoError(utx.Visit(fc))
	require.Equal(5640*units.MicroAvax, fc.Fee)

	ins := utx.Ins
	outs := utx.Outs
	importedIns := utx.ImportedIns
	require.Len(ins, 1)
	require.Len(importedIns, 1)
	require.Len(outs, 1)
	require.Equal(fc.Fee, importedIns[0].In.Amount()+ins[0].In.Amount()-outs[0].Out.Amount())
}

func TestExportTx(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	be := mocks.NewMockBuilderBackend(ctrl)

	var (
		utxosKey           = testKeys[1]
		utxoAddr           = utxosKey.PublicKey().Address()
		subnetID           = ids.GenerateTestID()
		utxos, avaxAssetID = testUTXOsList(utxosKey)

		exportedOutputs = []*avax.TransferableOutput{{
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 7 * units.Avax,
				OutputOwners: secp256k1fx.OutputOwners{
					Threshold: 1,
					Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
				},
			},
		}}
	)

	b := &DynamicFeesBuilder{
		addrs:   set.Of(utxoAddr),
		backend: be,
	}
	be.EXPECT().AVAXAssetID().Return(avaxAssetID).AnyTimes()
	be.EXPECT().NetworkID().Return(constants.MainnetID).AnyTimes()
	be.EXPECT().BlockchainID().Return(constants.PlatformChainID)
	be.EXPECT().UTXOs(gomock.Any(), constants.PlatformChainID).Return(utxos, nil)

	utx, err := b.NewExportTx(
		subnetID,
		exportedOutputs,
		testUnitFees,
		testBlockMaxConsumedUnits,
	)
	require.NoError(err)

	var (
		kc  = secp256k1fx.NewKeychain(utxosKey)
		sbe = mocks.NewMockSignerBackend(ctrl)
		s   = NewSigner(kc, sbe)
	)

	for _, utxo := range utxos {
		sbe.EXPECT().GetUTXO(gomock.Any(), gomock.Any(), utxo.InputID()).Return(utxo, nil).AnyTimes()
	}

	tx, err := s.SignUnsigned(stdcontext.Background(), utx)
	require.NoError(err)

	fc := &fees.Calculator{
		IsEForkActive:    true,
		Codec:            Parser.Codec(),
		FeeManager:       commonfees.NewManager(testUnitFees),
		ConsumedUnitsCap: testBlockMaxConsumedUnits,
		Credentials:      tx.Creds,
	}
	require.NoError(utx.Visit(fc))
	require.Equal(5966*units.MicroAvax, fc.Fee)

	ins := utx.Ins
	outs := utx.Outs
	require.Len(ins, 2)
	require.Len(outs, 1)
	require.Equal(fc.Fee+exportedOutputs[0].Out.Amount(), ins[0].In.Amount()+ins[1].In.Amount()-outs[0].Out.Amount())
	require.Equal(utx.ExportedOuts, exportedOutputs)
}

func testUTXOsList(utxosKey *secp256k1.PrivateKey) (
	[]*avax.UTXO,
	ids.ID, // avaxAssetID,
) {
	// Note: we avoid ids.GenerateTestNodeID here to make sure that UTXO IDs won't change
	// run by run. This simplifies checking what utxos are included in the built txs.
	utxosOffset := uint64(2024)

	var (
		avaxAssetID   = ids.Empty.Prefix(utxosOffset)
		subnetAssetID = ids.Empty.Prefix(utxosOffset + 1)
	)

	return []*avax.UTXO{ // currently, the wallet scans UTXOs in the order provided here
			{ // a small UTXO first, which  should not be enough to pay fees
				UTXOID: avax.UTXOID{
					TxID:        ids.Empty.Prefix(utxosOffset),
					OutputIndex: uint32(utxosOffset),
				},
				Asset: avax.Asset{ID: avaxAssetID},
				Out: &secp256k1fx.TransferOutput{
					Amt: 2 * units.MilliAvax,
					OutputOwners: secp256k1fx.OutputOwners{
						Locktime:  0,
						Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
						Threshold: 1,
					},
				},
			},
			{ // a locked, small UTXO
				UTXOID: avax.UTXOID{
					TxID:        ids.Empty.Prefix(utxosOffset + 1),
					OutputIndex: uint32(utxosOffset + 1),
				},
				Asset: avax.Asset{ID: avaxAssetID},
				Out: &stakeable.LockOut{
					Locktime: uint64(time.Now().Add(time.Hour).Unix()),
					TransferableOut: &secp256k1fx.TransferOutput{
						Amt: 3 * units.MilliAvax,
						OutputOwners: secp256k1fx.OutputOwners{
							Threshold: 1,
							Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
						},
					},
				},
			},
			{ // a subnetAssetID denominated UTXO
				UTXOID: avax.UTXOID{
					TxID:        ids.Empty.Prefix(utxosOffset + 2),
					OutputIndex: uint32(utxosOffset + 2),
				},
				Asset: avax.Asset{ID: subnetAssetID},
				Out: &secp256k1fx.TransferOutput{
					Amt: 99 * units.MegaAvax,
					OutputOwners: secp256k1fx.OutputOwners{
						Locktime:  0,
						Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
						Threshold: 1,
					},
				},
			},
			{ // a locked, large UTXO
				UTXOID: avax.UTXOID{
					TxID:        ids.Empty.Prefix(utxosOffset + 3),
					OutputIndex: uint32(utxosOffset + 3),
				},
				Asset: avax.Asset{ID: avaxAssetID},
				Out: &stakeable.LockOut{
					Locktime: uint64(time.Now().Add(time.Hour).Unix()),
					TransferableOut: &secp256k1fx.TransferOutput{
						Amt: 88 * units.Avax,
						OutputOwners: secp256k1fx.OutputOwners{
							Threshold: 1,
							Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
						},
					},
				},
			},
			{ // a large UTXO last, which should be enough to pay any fee by itself
				UTXOID: avax.UTXOID{
					TxID:        ids.Empty.Prefix(utxosOffset + 4),
					OutputIndex: uint32(utxosOffset + 4),
				},
				Asset: avax.Asset{ID: avaxAssetID},
				Out: &secp256k1fx.TransferOutput{
					Amt: 9 * units.Avax,
					OutputOwners: secp256k1fx.OutputOwners{
						Locktime:  0,
						Addrs:     []ids.ShortID{utxosKey.PublicKey().Address()},
						Threshold: 1,
					},
				},
			},
		},
		avaxAssetID
}