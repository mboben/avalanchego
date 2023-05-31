// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package validators

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/chains"
	"github.com/ava-labs/avalanchego/database"
	dbmanager "github.com/ava-labs/avalanchego/database/manager"
	"github.com/ava-labs/avalanchego/database/versiondb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/snow/uptime"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/formatting"
	"github.com/ava-labs/avalanchego/utils/json"
	"github.com/ava-labs/avalanchego/utils/timer/mockable"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/platformvm/api"
	"github.com/ava-labs/avalanchego/vms/platformvm/blocks"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/metrics"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
)

// AVAX asset ID in tests
var defaultRewardConfig = reward.Config{
	MaxConsumptionRate: .12 * reward.PercentDenominator,
	MinConsumptionRate: .10 * reward.PercentDenominator,
	MintingPeriod:      365 * 24 * time.Hour,
	SupplyCap:          720 * units.MegaAvax,
}

func TestVM_GetValidatorSet(t *testing.T) {
	// Populate the validator set to use below
	var (
		numVdrs       = 4
		vdrBaseWeight = uint64(1_000)
		vdrs          []*validators.Validator
	)

	for i := 0; i < numVdrs; i++ {
		sk, err := bls.NewSecretKey()
		require.NoError(t, err)

		vdrs = append(vdrs, &validators.Validator{
			NodeID:    ids.GenerateTestNodeID(),
			PublicKey: bls.PublicFromSecretKey(sk),
			Weight:    vdrBaseWeight + uint64(i),
		})
	}

	type test struct {
		name string
		// Height we're getting the diff at
		height             uint64
		lastAcceptedHeight uint64
		subnetID           ids.ID
		// Validator sets at tip
		currentPrimaryNetworkValidators []*validators.Validator
		currentSubnetValidators         []*validators.Validator
		// Diff at tip, block before tip, etc.
		// This must have [lastAcceptedHeight] - [height] elements
		weightDiffs []map[ids.NodeID]*state.ValidatorWeightDiff
		// Diff at tip, block before tip, etc.
		// This must have [lastAcceptedHeight] - [height] elements
		pkDiffs        []map[ids.NodeID]*bls.PublicKey
		expectedVdrSet map[ids.NodeID]*validators.GetValidatorOutput
		expectedErr    error
	}

	tests := []test{
		{
			name:               "after tip",
			height:             1,
			lastAcceptedHeight: 0,
			expectedVdrSet:     map[ids.NodeID]*validators.GetValidatorOutput{},
			expectedErr:        database.ErrNotFound,
		},
		{
			name:               "at tip",
			height:             1,
			lastAcceptedHeight: 1,
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
			},
			currentSubnetValidators: []*validators.Validator{
				copySubnetValidator(vdrs[0]),
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight,
				},
			},
			expectedErr: nil,
		},
		{
			name:               "1 before tip",
			height:             2,
			lastAcceptedHeight: 3,
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
				copyPrimaryValidator(vdrs[1]),
			},
			currentSubnetValidators: []*validators.Validator{
				// At tip we have these 2 validators
				copySubnetValidator(vdrs[0]),
				copySubnetValidator(vdrs[1]),
			},
			weightDiffs: []map[ids.NodeID]*state.ValidatorWeightDiff{
				{
					// At the tip block vdrs[0] lost weight, vdrs[1] gained weight,
					// and vdrs[2] left
					vdrs[0].NodeID: {
						Decrease: true,
						Amount:   1,
					},
					vdrs[1].NodeID: {
						Decrease: false,
						Amount:   1,
					},
					vdrs[2].NodeID: {
						Decrease: true,
						Amount:   vdrs[2].Weight,
					},
				},
			},
			pkDiffs: []map[ids.NodeID]*bls.PublicKey{
				{
					vdrs[2].NodeID: vdrs[2].PublicKey,
				},
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight + 1,
				},
				vdrs[1].NodeID: {
					NodeID:    vdrs[1].NodeID,
					PublicKey: vdrs[1].PublicKey,
					Weight:    vdrs[1].Weight - 1,
				},
				vdrs[2].NodeID: {
					NodeID:    vdrs[2].NodeID,
					PublicKey: vdrs[2].PublicKey,
					Weight:    vdrs[2].Weight,
				},
			},
			expectedErr: nil,
		},
		{
			name:               "2 before tip",
			height:             3,
			lastAcceptedHeight: 5,
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
				copyPrimaryValidator(vdrs[1]),
			},
			currentSubnetValidators: []*validators.Validator{
				// At tip we have these 2 validators
				copySubnetValidator(vdrs[0]),
				copySubnetValidator(vdrs[1]),
			},
			weightDiffs: []map[ids.NodeID]*state.ValidatorWeightDiff{
				{
					// At the tip block vdrs[0] lost weight, vdrs[1] gained weight,
					// and vdrs[2] left
					vdrs[0].NodeID: {
						Decrease: true,
						Amount:   1,
					},
					vdrs[1].NodeID: {
						Decrease: false,
						Amount:   1,
					},
					vdrs[2].NodeID: {
						Decrease: true,
						Amount:   vdrs[2].Weight,
					},
				},
				{
					// At the block before tip vdrs[0] lost weight, vdrs[1] gained weight,
					// vdrs[2] joined
					vdrs[0].NodeID: {
						Decrease: true,
						Amount:   1,
					},
					vdrs[1].NodeID: {
						Decrease: false,
						Amount:   1,
					},
					vdrs[2].NodeID: {
						Decrease: false,
						Amount:   vdrs[2].Weight,
					},
				},
			},
			pkDiffs: []map[ids.NodeID]*bls.PublicKey{
				{
					vdrs[2].NodeID: vdrs[2].PublicKey,
				},
				{},
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight + 2,
				},
				vdrs[1].NodeID: {
					NodeID:    vdrs[1].NodeID,
					PublicKey: vdrs[1].PublicKey,
					Weight:    vdrs[1].Weight - 2,
				},
			},
			expectedErr: nil,
		},
		{
			name:               "1 before tip; nil public key",
			height:             4,
			lastAcceptedHeight: 5,
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
				copyPrimaryValidator(vdrs[1]),
			},
			currentSubnetValidators: []*validators.Validator{
				// At tip we have these 2 validators
				copySubnetValidator(vdrs[0]),
				copySubnetValidator(vdrs[1]),
			},
			weightDiffs: []map[ids.NodeID]*state.ValidatorWeightDiff{
				{
					// At the tip block vdrs[0] lost weight, vdrs[1] gained weight,
					// and vdrs[2] left
					vdrs[0].NodeID: {
						Decrease: true,
						Amount:   1,
					},
					vdrs[1].NodeID: {
						Decrease: false,
						Amount:   1,
					},
					vdrs[2].NodeID: {
						Decrease: true,
						Amount:   vdrs[2].Weight,
					},
				},
			},
			pkDiffs: []map[ids.NodeID]*bls.PublicKey{
				{},
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight + 1,
				},
				vdrs[1].NodeID: {
					NodeID:    vdrs[1].NodeID,
					PublicKey: vdrs[1].PublicKey,
					Weight:    vdrs[1].Weight - 1,
				},
				vdrs[2].NodeID: {
					NodeID: vdrs[2].NodeID,
					Weight: vdrs[2].Weight,
				},
			},
			expectedErr: nil,
		},
		{
			name:               "1 before tip; subnet",
			height:             5,
			lastAcceptedHeight: 6,
			subnetID:           ids.GenerateTestID(),
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
				copyPrimaryValidator(vdrs[1]),
				copyPrimaryValidator(vdrs[3]),
			},
			currentSubnetValidators: []*validators.Validator{
				// At tip we have these 2 validators
				copySubnetValidator(vdrs[0]),
				copySubnetValidator(vdrs[1]),
			},
			weightDiffs: []map[ids.NodeID]*state.ValidatorWeightDiff{
				{
					// At the tip block vdrs[0] lost weight, vdrs[1] gained weight,
					// and vdrs[2] left
					vdrs[0].NodeID: {
						Decrease: true,
						Amount:   1,
					},
					vdrs[1].NodeID: {
						Decrease: false,
						Amount:   1,
					},
					vdrs[2].NodeID: {
						Decrease: true,
						Amount:   vdrs[2].Weight,
					},
				},
			},
			pkDiffs: []map[ids.NodeID]*bls.PublicKey{
				{},
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight + 1,
				},
				vdrs[1].NodeID: {
					NodeID:    vdrs[1].NodeID,
					PublicKey: vdrs[1].PublicKey,
					Weight:    vdrs[1].Weight - 1,
				},
				vdrs[2].NodeID: {
					NodeID: vdrs[2].NodeID,
					Weight: vdrs[2].Weight,
				},
			},
			expectedErr: nil,
		},
		{
			name:               "unrelated primary network key removal on subnet lookup",
			height:             4,
			lastAcceptedHeight: 5,
			subnetID:           ids.GenerateTestID(),
			currentPrimaryNetworkValidators: []*validators.Validator{
				copyPrimaryValidator(vdrs[0]),
			},
			currentSubnetValidators: []*validators.Validator{
				copySubnetValidator(vdrs[0]),
			},
			weightDiffs: []map[ids.NodeID]*state.ValidatorWeightDiff{
				{},
			},
			pkDiffs: []map[ids.NodeID]*bls.PublicKey{
				{
					vdrs[1].NodeID: vdrs[1].PublicKey,
				},
			},
			expectedVdrSet: map[ids.NodeID]*validators.GetValidatorOutput{
				vdrs[0].NodeID: {
					NodeID:    vdrs[0].NodeID,
					PublicKey: vdrs[0].PublicKey,
					Weight:    vdrs[0].Weight,
				},
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// setup validators set
			vdrs := validators.NewMockManager(ctrl)
			cfg := config.Config{
				Chains:                 chains.TestManager,
				UptimePercentage:       .2,
				RewardConfig:           defaultRewardConfig,
				Validators:             vdrs,
				UptimeLockedCalculator: uptime.NewLockedCalculator(),
				BanffTime:              mockable.MaxTime,
			}
			mockState := state.NewMockState(ctrl)

			metrics, err := metrics.New("", prometheus.NewRegistry(), cfg.TrackedSubnets)
			r.NoError(err)

			clk := &mockable.Clock{}
			validatorssSet := NewManager(cfg, mockState, metrics, clk)

			// Mock the VM's validators
			mockSubnetVdrSet := validators.NewMockSet(ctrl)
			mockSubnetVdrSet.EXPECT().List().Return(tt.currentSubnetValidators).AnyTimes()
			vdrs.EXPECT().Get(tt.subnetID).Return(mockSubnetVdrSet, true).AnyTimes()

			mockPrimaryVdrSet := mockSubnetVdrSet
			if tt.subnetID != constants.PrimaryNetworkID {
				mockPrimaryVdrSet = validators.NewMockSet(ctrl)
				vdrs.EXPECT().Get(constants.PrimaryNetworkID).Return(mockPrimaryVdrSet, true).AnyTimes()
			}
			for _, vdr := range tt.currentPrimaryNetworkValidators {
				mockPrimaryVdrSet.EXPECT().Get(vdr.NodeID).Return(vdr, true).AnyTimes()
			}

			// Tell state what diffs to report
			for _, weightDiff := range tt.weightDiffs {
				mockState.EXPECT().GetValidatorWeightDiffs(gomock.Any(), gomock.Any()).Return(weightDiff, nil)
			}

			for _, pkDiff := range tt.pkDiffs {
				mockState.EXPECT().GetValidatorPublicKeyDiffs(gomock.Any()).Return(pkDiff, nil)
			}

			// Tell state last accepted block to report
			mockTip := blocks.NewMockBlock(ctrl)
			mockTip.EXPECT().Height().Return(tt.lastAcceptedHeight)
			mockTipID := ids.GenerateTestID()
			mockState.EXPECT().GetLastAccepted().Return(mockTipID)
			mockState.EXPECT().GetStatelessBlock(mockTipID).Return(mockTip, choices.Accepted, nil)

			// Compute validator set at previous height
			gotVdrSet, err := validatorssSet.GetValidatorSet(context.Background(), tt.height, tt.subnetID)
			r.ErrorIs(err, tt.expectedErr)
			if tt.expectedErr != nil {
				return
			}
			r.Equal(tt.expectedVdrSet, gotVdrSet)
		})
	}
}

func Test_RegressionBLSKeyDiff(t *testing.T) {
	// create manager from empty state
	require := require.New(t)

	var (
		cfg = defaultConfig()
		clk = &mockable.Clock{}
		ctx = defaultCtx()
	)

	baseDBManager := dbmanager.NewMemDB(version.Semantic1_0_0)
	db := versiondb.New(baseDBManager.Current().Database)
	rewardsCalc := reward.NewCalculator(cfg.RewardConfig)
	genesisBytes := buildGenesisTest(ctx)
	state, err := state.New(
		db,
		genesisBytes,
		prometheus.NewRegistry(),
		&cfg,
		ctx,
		metrics.Noop,
		rewardsCalc,
		&utils.Atomic[bool]{},
	)
	require.NoError(err)

	valMan := NewManager(cfg, state, metrics.Noop, clk)

	height, err := valMan.GetCurrentHeight(context.Background())
	require.NoError(err)
	require.Equal(uint64(0), height)
}

func defaultConfig() config.Config {
	vdrs := validators.NewManager()
	primaryVdrs := validators.NewSet()
	_ = vdrs.Add(constants.PrimaryNetworkID, primaryVdrs)

	defaultTxFee := uint64(100)
	defaultMinStakingDuration := 24 * time.Hour
	defaultMaxStakingDuration := 365 * 24 * time.Hour

	return config.Config{
		Chains:                 chains.TestManager,
		UptimeLockedCalculator: uptime.NewLockedCalculator(),
		Validators:             vdrs,
		TxFee:                  defaultTxFee,
		CreateSubnetTxFee:      100 * defaultTxFee,
		CreateBlockchainTxFee:  100 * defaultTxFee,
		MinValidatorStake:      5 * units.MilliAvax,
		MaxValidatorStake:      500 * units.MilliAvax,
		MinDelegatorStake:      1 * units.MilliAvax,
		MinStakeDuration:       defaultMinStakingDuration,
		MaxStakeDuration:       defaultMaxStakingDuration,
		RewardConfig:           defaultRewardConfig,
	}
}

func defaultCtx() *snow.Context {
	ctx := snow.DefaultContextTest()

	var (
		avaxAssetID = ids.ID{'y', 'e', 'e', 't'}
		xChainID    = ids.Empty.Prefix(0)
		cChainID    = ids.Empty.Prefix(1)
	)

	ctx.NetworkID = 10
	ctx.XChainID = xChainID
	ctx.CChainID = cChainID
	ctx.AVAXAssetID = avaxAssetID
	ctx.ValidatorState = &validators.TestState{
		GetSubnetIDF: func(_ context.Context, chainID ids.ID) (ids.ID, error) {
			subnetID, ok := map[ids.ID]ids.ID{
				constants.PlatformChainID: constants.PrimaryNetworkID,
				xChainID:                  constants.PrimaryNetworkID,
				cChainID:                  constants.PrimaryNetworkID,
			}[chainID]
			if !ok {
				return ids.Empty, errors.New("missing")
			}
			return subnetID, nil
		},
	}

	return ctx
}

func buildGenesisTest(ctx *snow.Context) []byte {
	// empty genesis, no validators nor utxos.
	genesisUTXOs := make([]api.UTXO, 0)
	genesisValidators := make([]api.PermissionlessValidator, 0)
	defaultGenesisTime := time.Date(1997, 1, 1, 0, 0, 0, 0, time.UTC)

	buildGenesisArgs := api.BuildGenesisArgs{
		NetworkID:     json.Uint32(constants.UnitTestID),
		AvaxAssetID:   ctx.AVAXAssetID,
		UTXOs:         genesisUTXOs,
		Validators:    genesisValidators,
		Chains:        nil,
		Time:          json.Uint64(defaultGenesisTime.Unix()),
		InitialSupply: json.Uint64(360 * units.MegaAvax),
		Encoding:      formatting.Hex,
	}

	buildGenesisResponse := api.BuildGenesisReply{}
	platformvmSS := api.StaticService{}
	if err := platformvmSS.BuildGenesis(nil, &buildGenesisArgs, &buildGenesisResponse); err != nil {
		panic(fmt.Errorf("problem while building platform chain's genesis state: %w", err))
	}

	genesisBytes, err := formatting.Decode(buildGenesisResponse.Encoding, buildGenesisResponse.Bytes)
	if err != nil {
		panic(err)
	}

	return genesisBytes
}

func copyPrimaryValidator(vdr *validators.Validator) *validators.Validator {
	newVdr := *vdr
	return &newVdr
}

func copySubnetValidator(vdr *validators.Validator) *validators.Validator {
	newVdr := *vdr
	newVdr.PublicKey = nil
	return &newVdr
}
