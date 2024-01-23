// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package fees

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/snowtest"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/components/fees"
	"github.com/ava-labs/avalanchego/vms/components/verify"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/signer"
	"github.com/ava-labs/avalanchego/vms/platformvm/stakeable"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
)

var (
	testUnitFees = fees.Dimensions{
		1 * units.MicroAvax,
		2 * units.MicroAvax,
		3 * units.MicroAvax,
		4 * units.MicroAvax,
	}
	testBlockMaxConsumedUnits = fees.Dimensions{
		3000,
		3500,
		1000,
		1000,
	}

	feeTestsDefaultCfg = config.Config{
		TxFee:                         1 * units.Avax,
		CreateAssetTxFee:              2 * units.Avax,
		CreateSubnetTxFee:             3 * units.Avax,
		TransformSubnetTxFee:          4 * units.Avax,
		CreateBlockchainTxFee:         5 * units.Avax,
		AddPrimaryNetworkValidatorFee: 6 * units.Avax,
		AddPrimaryNetworkDelegatorFee: 7 * units.Avax,
		AddSubnetValidatorFee:         8 * units.Avax,
		AddSubnetDelegatorFee:         9 * units.Avax,
	}

	preFundedKeys             = secp256k1.TestKeys()
	feeTestSigners            = [][]*secp256k1.PrivateKey{preFundedKeys}
	feeTestDefaultStakeWeight = uint64(2024)
	durangoTime               = time.Time{} // assume durango is active in these tests
)

type feeTests struct {
	description       string
	cfgAndChainTimeF  func() (*config.Config, time.Time)
	consumedUnitCapsF func() fees.Dimensions
	expectedError     error
	checksF           func(*testing.T, *Calculator)
}

func TestPartiallyFulledTransactionsSizes(t *testing.T) {
	var uTx *txs.AddValidatorTx
	uTxSize, err := txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 2)

	uTx = &txs.AddValidatorTx{}
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 102)

	// array of nil elements has size 0.
	creds := make([]verify.Verifiable, 10)
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, creds)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 6)

	creds[0] = &secp256k1fx.Credential{
		Sigs: make([][secp256k1.SignatureLen]byte, 5),
	}
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, creds)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 339)

	var sTx *txs.Tx
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, sTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 2)

	sTx = &txs.Tx{}
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, sTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 6)

	sTx = &txs.Tx{
		Unsigned: uTx,
	}
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, sTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 110)

	sTx = &txs.Tx{
		Unsigned: uTx,
		Creds:    creds,
	}
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, sTx)
	require.NoError(t, err)
	require.Equal(t, uTxSize, 443)
}

func TestAddAndRemoveFees(t *testing.T) {
	r := require.New(t)

	fc := &Calculator{
		IsEForkActive:    true,
		FeeManager:       fees.NewManager(testUnitFees),
		ConsumedUnitsCap: testBlockMaxConsumedUnits,
	}

	units := fees.Dimensions{
		1,
		2,
		3,
		4,
	}

	r.NoError(fc.AddFeesFor(units))
	r.Equal(units, fc.FeeManager.GetCumulatedUnits())
	r.NotZero(fc.Fee)

	r.NoError(fc.RemoveFeesFor(units))
	r.Zero(fc.FeeManager.GetCumulatedUnits())
	r.Zero(fc.Fee)
}

func TestUTXOsAreAdditiveInSize(t *testing.T) {
	// Show that including utxos of size [S] into a tx of size [T]
	// result in a tx of size [S+T-CodecVersion]
	// This is key to calculate fees correctly while building a tx

	uTx := &txs.AddValidatorTx{
		BaseTx: txs.BaseTx{
			BaseTx: avax.BaseTx{
				NetworkID:    rand.Uint32(), //#nosec G404
				BlockchainID: ids.GenerateTestID(),
				Memo:         []byte{'a', 'b', 'c'},
				Ins:          make([]*avax.TransferableInput, 0),
				Outs:         make([]*avax.TransferableOutput, 0),
			},
		},
	}

	uTxNakedSize := 105
	uTxSize, err := txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxNakedSize, uTxSize)

	// input to add
	input := &avax.TransferableInput{
		UTXOID: avax.UTXOID{
			TxID:        ids.ID{'t', 'x', 'I', 'D'},
			OutputIndex: 2,
		},
		Asset: avax.Asset{ID: ids.GenerateTestID()},
		In: &secp256k1fx.TransferInput{
			Amt:   uint64(5678),
			Input: secp256k1fx.Input{SigIndices: []uint32{0}},
		},
	}
	inSize, err := txs.Codec.Size(txs.CodecVersion, input)
	require.NoError(t, err)

	// include input in uTx and check that sizes add
	uTx.BaseTx.BaseTx.Ins = append(uTx.BaseTx.BaseTx.Ins, input)
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxNakedSize+(inSize-codec.CodecVersionSize), uTxSize)

	// output to add
	output := &avax.TransferableOutput{
		Asset: avax.Asset{
			ID: ids.GenerateTestID(),
		},
		Out: &stakeable.LockOut{
			Locktime: 87654321,
			TransferableOut: &secp256k1fx.TransferOutput{
				Amt: 1,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  12345678,
					Threshold: 0,
					Addrs:     []ids.ShortID{},
				},
			},
		},
	}
	outSize, err := txs.Codec.Size(txs.CodecVersion, output)
	require.NoError(t, err)

	// include output in uTx and check that sizes add
	uTx.BaseTx.BaseTx.Outs = append(uTx.BaseTx.BaseTx.Outs, output)
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxNakedSize+(inSize-codec.CodecVersionSize)+(outSize-codec.CodecVersionSize), uTxSize)

	// include output in uTx as stake and check that sizes add
	uTx.StakeOuts = append(uTx.StakeOuts, output)
	uTxSize, err = txs.Codec.Size(txs.CodecVersion, uTx)
	require.NoError(t, err)
	require.Equal(t, uTxNakedSize+(inSize-codec.CodecVersionSize)+(outSize-codec.CodecVersionSize)+(outSize-codec.CodecVersionSize), uTxSize)
}

func TestAddValidatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, stakes, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.AddValidatorTx{
		BaseTx: baseTx,
		Validator: txs.Validator{
			NodeID: defaultCtx.NodeID,
			Start:  uint64(time.Now().Truncate(time.Second).Unix()),
			End:    uint64(time.Now().Truncate(time.Second).Add(time.Hour).Unix()),
			Wght:   feeTestDefaultStakeWeight,
		},
		StakeOuts: stakes,
		RewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{ids.GenerateTestShortID()},
		},
		DelegationShares: reward.PercentDenominator,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.AddPrimaryNetworkValidatorFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3719*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						741,
						1090,
						266,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, bandwidth cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.Bandwidth] = 741 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestAddSubnetValidatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	subnetID := ids.GenerateTestID()
	baseTx, _, subnetAuth := txsCreationHelpers(defaultCtx)
	uTx := &txs.AddSubnetValidatorTx{
		BaseTx: baseTx,
		SubnetValidator: txs.SubnetValidator{
			Validator: txs.Validator{
				NodeID: defaultCtx.NodeID,
				Start:  uint64(time.Now().Truncate(time.Second).Unix()),
				End:    uint64(time.Now().Truncate(time.Second).Add(time.Hour).Unix()),
				Wght:   feeTestDefaultStakeWeight,
			},
			Subnet: subnetID,
		},
		SubnetAuth: subnetAuth,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.AddSubnetValidatorFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3345*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						649,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestAddDelegatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, stakes, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.AddDelegatorTx{
		BaseTx: baseTx,
		Validator: txs.Validator{
			NodeID: defaultCtx.NodeID,
			Start:  uint64(time.Now().Truncate(time.Second).Unix()),
			End:    uint64(time.Now().Truncate(time.Second).Add(time.Hour).Unix()),
			Wght:   feeTestDefaultStakeWeight,
		},
		StakeOuts: stakes,
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{preFundedKeys[0].PublicKey().Address()},
		},
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.AddPrimaryNetworkDelegatorFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3715*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						737,
						1090,
						266,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestCreateChainTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, subnetAuth := txsCreationHelpers(defaultCtx)
	uTx := &txs.CreateChainTx{
		BaseTx:      baseTx,
		SubnetID:    ids.GenerateTestID(),
		ChainName:   "testingStuff",
		VMID:        ids.GenerateTestID(),
		FxIDs:       []ids.ID{ids.GenerateTestID()},
		GenesisData: []byte{0xff},
		SubnetAuth:  subnetAuth,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.CreateBlockchainTxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3388*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						692,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestCreateSubnetTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.CreateSubnetTx{
		BaseTx: baseTx,
		Owner: &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{preFundedKeys[0].PublicKey().Address()},
		},
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.CreateSubnetTxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3293*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						597,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestRemoveSubnetValidatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, auth := txsCreationHelpers(defaultCtx)
	uTx := &txs.RemoveSubnetValidatorTx{
		BaseTx:     baseTx,
		NodeID:     ids.GenerateTestNodeID(),
		Subnet:     ids.GenerateTestID(),
		SubnetAuth: auth,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3321*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						625,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestTransformSubnetTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, auth := txsCreationHelpers(defaultCtx)
	uTx := &txs.TransformSubnetTx{
		BaseTx:                   baseTx,
		Subnet:                   ids.GenerateTestID(),
		AssetID:                  ids.GenerateTestID(),
		InitialSupply:            0x1000000000000000,
		MaximumSupply:            0x1000000000000000,
		MinConsumptionRate:       0,
		MaxConsumptionRate:       0,
		MinValidatorStake:        1,
		MaxValidatorStake:        0x1000000000000000,
		MinStakeDuration:         1,
		MaxStakeDuration:         1,
		MinDelegationFee:         0,
		MinDelegatorStake:        0xffffffffffffffff,
		MaxValidatorWeightFactor: 255,
		UptimeRequirement:        0,
		SubnetAuth:               auth,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TransformSubnetTxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3406*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						710,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestTransferSubnetOwnershipTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.TransferSubnetOwnershipTx{
		BaseTx: baseTx,
		Subnet: ids.GenerateTestID(),
		SubnetAuth: &secp256k1fx.Input{
			SigIndices: []uint32{3},
		},
		Owner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs: []ids.ShortID{
				ids.GenerateTestShortID(),
			},
		},
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3337*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						641,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestAddPermissionlessValidatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, stakes, _ := txsCreationHelpers(defaultCtx)
	sk, err := bls.NewSecretKey()
	r.NoError(err)
	uTx := &txs.AddPermissionlessValidatorTx{
		BaseTx:    baseTx,
		Subnet:    ids.GenerateTestID(),
		Signer:    signer.NewProofOfPossession(sk),
		StakeOuts: stakes,
		ValidatorRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs: []ids.ShortID{
				ids.GenerateTestShortID(),
			},
		},
		DelegatorRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs: []ids.ShortID{
				ids.GenerateTestShortID(),
			},
		},
		DelegationShares: reward.PercentDenominator,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.AddSubnetValidatorFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3939*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						961,
						1090,
						266,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestAddPermissionlessDelegatorTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, stakes, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.AddPermissionlessDelegatorTx{
		BaseTx: baseTx,
		Validator: txs.Validator{
			NodeID: ids.GenerateTestNodeID(),
			Start:  12345,
			End:    12345 + 200*24*60*60,
			Wght:   2 * units.KiloAvax,
		},
		Subnet:    ids.GenerateTestID(),
		StakeOuts: stakes,
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs: []ids.ShortID{
				ids.GenerateTestShortID(),
			},
		},
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.AddSubnetDelegatorFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3747*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						769,
						1090,
						266,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestBaseTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, _ := txsCreationHelpers(defaultCtx)
	uTx := &baseTx
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3253*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						557,
						1090,
						172,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestImportTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, _, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.ImportTx{
		BaseTx:      baseTx,
		SourceChain: ids.GenerateTestID(),
		ImportedInputs: []*avax.TransferableInput{{
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(1),
				OutputIndex: 1,
			},
			Asset: avax.Asset{ID: ids.ID{'a', 's', 's', 'e', 'r', 't'}},
			In: &secp256k1fx.TransferInput{
				Amt:   50000,
				Input: secp256k1fx.Input{SigIndices: []uint32{0}},
			},
		}},
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 5827*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						681,
						2180,
						262,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func TestExportTxFees(t *testing.T) {
	r := require.New(t)

	defaultCtx := snowtest.Context(t, snowtest.PChainID)

	baseTx, outputs, _ := txsCreationHelpers(defaultCtx)
	uTx := &txs.ExportTx{
		BaseTx:           baseTx,
		DestinationChain: ids.GenerateTestID(),
		ExportedOutputs:  outputs,
	}
	sTx, err := txs.NewSigned(uTx, txs.Codec, feeTestSigners)
	r.NoError(err)

	tests := []feeTests{
		{
			description: "pre E fork",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(-1 * time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, fc.Config.TxFee, fc.Fee)
			},
		},
		{
			description: "post E fork, success",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			expectedError: nil,
			checksF: func(t *testing.T, fc *Calculator) {
				require.Equal(t, 3663*units.MicroAvax, fc.Fee)
				require.Equal(t,
					fees.Dimensions{
						685,
						1090,
						266,
						0,
					},
					fc.FeeManager.GetCumulatedUnits(),
				)
			},
		},
		{
			description: "post E fork, utxos read cap breached",
			cfgAndChainTimeF: func() (*config.Config, time.Time) {
				eForkTime := time.Now().Truncate(time.Second)
				chainTime := eForkTime.Add(time.Second)

				cfg := feeTestsDefaultCfg
				cfg.DurangoTime = durangoTime
				cfg.EForkTime = eForkTime

				return &cfg, chainTime
			},
			consumedUnitCapsF: func() fees.Dimensions {
				caps := testBlockMaxConsumedUnits
				caps[fees.UTXORead] = 1090 - 1
				return caps
			},
			expectedError: errFailedConsumedUnitsCumulation,
			checksF:       func(t *testing.T, fc *Calculator) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cfg, chainTime := tt.cfgAndChainTimeF()

			consumedUnitCaps := testBlockMaxConsumedUnits
			if tt.consumedUnitCapsF != nil {
				consumedUnitCaps = tt.consumedUnitCapsF()
			}

			fc := &Calculator{
				IsEForkActive:    cfg.IsEForkActivated(chainTime),
				Config:           cfg,
				ChainTime:        chainTime,
				FeeManager:       fees.NewManager(testUnitFees),
				ConsumedUnitsCap: consumedUnitCaps,
				Credentials:      sTx.Creds,
			}
			err := uTx.Visit(fc)
			r.ErrorIs(err, tt.expectedError)
			tt.checksF(t, fc)
		})
	}
}

func txsCreationHelpers(defaultCtx *snow.Context) (
	baseTx txs.BaseTx,
	stakes []*avax.TransferableOutput,
	auth *secp256k1fx.Input,
) {
	inputs := []*avax.TransferableInput{{
		UTXOID: avax.UTXOID{
			TxID:        ids.ID{'t', 'x', 'I', 'D'},
			OutputIndex: 2,
		},
		Asset: avax.Asset{ID: defaultCtx.AVAXAssetID},
		In: &secp256k1fx.TransferInput{
			Amt:   uint64(5678),
			Input: secp256k1fx.Input{SigIndices: []uint32{0}},
		},
	}}
	outputs := []*avax.TransferableOutput{{
		Asset: avax.Asset{ID: defaultCtx.AVAXAssetID},
		Out: &secp256k1fx.TransferOutput{
			Amt: uint64(1234),
			OutputOwners: secp256k1fx.OutputOwners{
				Threshold: 1,
				Addrs:     []ids.ShortID{preFundedKeys[0].PublicKey().Address()},
			},
		},
	}}
	stakes = []*avax.TransferableOutput{{
		Asset: avax.Asset{ID: defaultCtx.AVAXAssetID},
		Out: &stakeable.LockOut{
			Locktime: uint64(time.Now().Add(time.Second).Unix()),
			TransferableOut: &secp256k1fx.TransferOutput{
				Amt: feeTestDefaultStakeWeight,
				OutputOwners: secp256k1fx.OutputOwners{
					Threshold: 1,
					Addrs:     []ids.ShortID{preFundedKeys[0].PublicKey().Address()},
				},
			},
		},
	}}
	auth = &secp256k1fx.Input{
		SigIndices: []uint32{0, 1},
	}
	baseTx = txs.BaseTx{
		BaseTx: avax.BaseTx{
			NetworkID:    defaultCtx.NetworkID,
			BlockchainID: defaultCtx.ChainID,
			Ins:          inputs,
			Outs:         outputs,
		},
	}

	return baseTx, stakes, auth
}