// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package executor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/hashing"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/builder"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/fees"
	"github.com/ava-labs/avalanchego/vms/platformvm/utxo"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/chain/p/backends"

	commonfees "github.com/ava-labs/avalanchego/vms/components/fees"
)

// Ensure Execute fails when there are not enough control sigs
func TestCreateChainTxInsufficientControlSigs(t *testing.T) {
	require := require.New(t)
	env := newEnvironment(t, banff)
	env.ctx.Lock.Lock()
	defer env.ctx.Lock.Unlock()

	tx, err := env.txBuilder.NewCreateChainTx(
		testSubnet1.ID(),
		nil,
		constants.AVMID,
		nil,
		"chain name",
		[]*secp256k1.PrivateKey{preFundedKeys[0], preFundedKeys[1]},
		ids.ShortEmpty,
		nil,
	)
	require.NoError(err)

	// Remove a signature
	tx.Creds[0].(*secp256k1fx.Credential).Sigs = tx.Creds[0].(*secp256k1fx.Credential).Sigs[1:]

	stateDiff, err := state.NewDiff(lastAcceptedID, env)
	require.NoError(err)

	feeCfg := env.config.GetDynamicFeesConfig(env.state.GetTimestamp())
	executor := StandardTxExecutor{
		Backend:       &env.backend,
		BlkFeeManager: commonfees.NewManager(feeCfg.UnitFees),
		UnitCaps:      feeCfg.BlockUnitsCap,
		State:         stateDiff,
		Tx:            tx,
	}
	err = tx.Unsigned.Visit(&executor)
	require.ErrorIs(err, errUnauthorizedSubnetModification)
}

// Ensure Execute fails when an incorrect control signature is given
func TestCreateChainTxWrongControlSig(t *testing.T) {
	require := require.New(t)
	env := newEnvironment(t, banff)
	env.ctx.Lock.Lock()
	defer env.ctx.Lock.Unlock()

	tx, err := env.txBuilder.NewCreateChainTx(
		testSubnet1.ID(),
		nil,
		constants.AVMID,
		nil,
		"chain name",
		[]*secp256k1.PrivateKey{testSubnet1ControlKeys[0], testSubnet1ControlKeys[1]},
		ids.ShortEmpty,
		nil,
	)
	require.NoError(err)

	// Generate new, random key to sign tx with
	key, err := secp256k1.NewPrivateKey()
	require.NoError(err)

	// Replace a valid signature with one from another key
	sig, err := key.SignHash(hashing.ComputeHash256(tx.Unsigned.Bytes()))
	require.NoError(err)
	copy(tx.Creds[0].(*secp256k1fx.Credential).Sigs[0][:], sig)

	stateDiff, err := state.NewDiff(lastAcceptedID, env)
	require.NoError(err)

	feeCfg := env.config.GetDynamicFeesConfig(stateDiff.GetTimestamp())
	executor := StandardTxExecutor{
		Backend:       &env.backend,
		BlkFeeManager: commonfees.NewManager(feeCfg.UnitFees),
		UnitCaps:      feeCfg.BlockUnitsCap,
		State:         stateDiff,
		Tx:            tx,
	}
	err = tx.Unsigned.Visit(&executor)
	require.ErrorIs(err, errUnauthorizedSubnetModification)
}

// Ensure Execute fails when the Subnet the blockchain specifies as
// its validator set doesn't exist
func TestCreateChainTxNoSuchSubnet(t *testing.T) {
	require := require.New(t)
	env := newEnvironment(t, eUpgrade)
	env.ctx.Lock.Lock()
	defer env.ctx.Lock.Unlock()

	tx, err := env.txBuilder.NewCreateChainTx(
		testSubnet1.ID(),
		nil,
		constants.AVMID,
		nil,
		"chain name",
		[]*secp256k1.PrivateKey{testSubnet1ControlKeys[0], testSubnet1ControlKeys[1]},
		ids.ShortEmpty,
		nil,
	)
	require.NoError(err)

	tx.Unsigned.(*txs.CreateChainTx).SubnetID = ids.GenerateTestID()

	stateDiff, err := state.NewDiff(lastAcceptedID, env)
	require.NoError(err)

	feeCfg := env.config.GetDynamicFeesConfig(stateDiff.GetTimestamp())
	executor := StandardTxExecutor{
		Backend:       &env.backend,
		BlkFeeManager: commonfees.NewManager(feeCfg.UnitFees),
		UnitCaps:      feeCfg.BlockUnitsCap,
		State:         stateDiff,
		Tx:            tx,
	}
	err = tx.Unsigned.Visit(&executor)
	require.ErrorIs(err, database.ErrNotFound)
}

// Ensure valid tx passes semanticVerify
func TestCreateChainTxValid(t *testing.T) {
	require := require.New(t)
	env := newEnvironment(t, eUpgrade)
	env.ctx.Lock.Lock()
	defer env.ctx.Lock.Unlock()

	tx, err := env.txBuilder.NewCreateChainTx(
		testSubnet1.ID(),
		nil,
		constants.AVMID,
		nil,
		"chain name",
		[]*secp256k1.PrivateKey{testSubnet1ControlKeys[0], testSubnet1ControlKeys[1]},
		ids.ShortEmpty,
		nil,
	)
	require.NoError(err)

	stateDiff, err := state.NewDiff(lastAcceptedID, env)
	require.NoError(err)

	feeCfg := env.config.GetDynamicFeesConfig(stateDiff.GetTimestamp())
	executor := StandardTxExecutor{
		Backend:       &env.backend,
		BlkFeeManager: commonfees.NewManager(feeCfg.UnitFees),
		UnitCaps:      feeCfg.BlockUnitsCap,
		State:         stateDiff,
		Tx:            tx,
	}
	require.NoError(tx.Unsigned.Visit(&executor))
}

func TestCreateChainTxAP3FeeChange(t *testing.T) {
	ap3Time := defaultGenesisTime.Add(time.Hour)
	tests := []struct {
		name          string
		time          time.Time
		fee           uint64
		expectedError error
	}{
		{
			name:          "pre-fork - correctly priced",
			time:          defaultGenesisTime,
			fee:           0,
			expectedError: nil,
		},
		{
			name:          "post-fork - incorrectly priced",
			time:          ap3Time,
			fee:           100*defaultTxFee - 1*units.NanoAvax,
			expectedError: utxo.ErrInsufficientUnlockedFunds,
		},
		{
			name:          "post-fork - correctly priced",
			time:          ap3Time,
			fee:           100 * defaultTxFee,
			expectedError: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)

			env := newEnvironment(t, banff)
			env.config.ApricotPhase3Time = ap3Time

			addrs := set.NewSet[ids.ShortID](len(preFundedKeys))
			for _, key := range preFundedKeys {
				addrs.Add(key.Address())
			}

			env.state.SetTimestamp(test.time) // to duly set fee

			cfg := *env.config
			cfg.CreateBlockchainTxFee = test.fee

			var (
				backend  = builder.NewBackend(env.ctx, &cfg, env.state, env.atomicUTXOs)
				pBuilder = backends.NewBuilder(addrs, backend)
				feeCfg   = cfg.GetDynamicFeesConfig(env.state.GetTimestamp())
				feeCalc  = &fees.Calculator{
					IsEUpgradeActive: false,
					Config:           &cfg,
					ChainTime:        test.time,
					FeeManager:       commonfees.NewManager(feeCfg.UnitFees),
					ConsumedUnitsCap: feeCfg.BlockUnitsCap,
				}
			)
			backend.ResetAddresses(addrs)

			utx, err := pBuilder.NewCreateChainTx(
				testSubnet1.ID(),
				nil,                  // genesisData
				ids.GenerateTestID(), // vmID
				nil,                  // fxIDs
				"",                   // chainName
				feeCalc,
			)
			require.NoError(err)

			kc := secp256k1fx.NewKeychain(preFundedKeys...)
			s := backends.NewSigner(kc, backend)
			tx, err := backends.SignUnsigned(context.Background(), s, utx)
			require.NoError(err)

			stateDiff, err := state.NewDiff(lastAcceptedID, env)
			require.NoError(err)

			stateDiff.SetTimestamp(test.time)

			feeCfg = env.config.GetDynamicFeesConfig(stateDiff.GetTimestamp())
			executor := StandardTxExecutor{
				Backend:       &env.backend,
				BlkFeeManager: commonfees.NewManager(feeCfg.UnitFees),
				UnitCaps:      feeCfg.BlockUnitsCap,
				State:         stateDiff,
				Tx:            tx,
			}
			err = tx.Unsigned.Visit(&executor)
			require.ErrorIs(err, test.expectedError)
		})
	}
}
