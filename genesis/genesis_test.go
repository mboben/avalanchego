// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package genesis

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "embed"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/hashing"
	"github.com/ava-labs/avalanchego/utils/perms"
	"github.com/ava-labs/avalanchego/vms/platformvm/genesis"
)

var (
	//go:embed genesis_test.json
	customGenesisConfigJSON  []byte
	invalidGenesisConfigJSON = []byte(`{
		"networkID": 9999}}}}
	}`)

	genesisStakingCfg = &StakingConfig{
		MaxStakeDuration: 365 * 24 * time.Hour,
	}
)

func TestValidateConfig(t *testing.T) {
	tests := map[string]struct {
		networkID   uint32
		config      *Config
		expectedErr error
	}{
		"mainnet": {
			networkID:   1,
			config:      &MainnetConfig,
			expectedErr: nil,
		},
		"fuji": {
			networkID:   5,
			config:      &FujiConfig,
			expectedErr: nil,
		},
		"local": {
			networkID: 162,
			config:    &LocalFlareConfig,
		},
		"mainnet (networkID mismatch)": {
			networkID:   2,
			config:      &MainnetConfig,
			expectedErr: errConflictingNetworkIDs,
		},
		"invalid start time": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.StartTime = 999999999999999
				return &thisConfig
			}(),
			expectedErr: errFutureStartTime,
		},
		"no initial supply": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.Allocations = []Allocation{}
				return &thisConfig
			}(),
			expectedErr: errNoSupply,
		},
		"no initial stakers": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.InitialStakers = []Staker{}
				return &thisConfig
			}(),
			expectedErr: errNoStakers,
		},
		"invalid initial stake duration": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.InitialStakeDuration = 0
				return &thisConfig
			}(),
			expectedErr: errNoStakeDuration,
		},
		"too large initial stake duration": {
			networkID: 12345,
			config: func() *Config {
				thisConfig := LocalConfig
				thisConfig.InitialStakeDuration = uint64(genesisStakingCfg.MaxStakeDuration+time.Second) / uint64(time.Second)
				return &thisConfig
			}(),
			expectedErr: errStakeDurationTooHigh,
		},
		"invalid stake offset": {
			networkID: 14,
			config: func() *Config {
				thisConfig := FlareConfig
				thisConfig.InitialStakeDurationOffset = 100000000
				return &thisConfig
			}(),
			expectedErr: errInitialStakeDurationTooLow,
		},
		"empty initial staked funds": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.InitialStakedFunds = []ids.ShortID(nil)
				return &thisConfig
			}(),
			expectedErr: errNoInitiallyStakedFunds,
		},
		"duplicate initial staked funds": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.InitialStakedFunds = append(thisConfig.InitialStakedFunds, thisConfig.InitialStakedFunds[0])
				return &thisConfig
			}(),
			expectedErr: errDuplicateInitiallyStakedAddress,
		},
		"initial staked funds not in allocations": {
			networkID: 5,
			config: func() *Config {
				thisConfig := FujiConfig
				thisConfig.InitialStakedFunds = append(thisConfig.InitialStakedFunds, LocalFlareConfig.InitialStakedFunds[0])
				return &thisConfig
			}(),
			expectedErr: errNoAllocationToStake,
		},
		"empty C-Chain genesis": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.CChainGenesis = ""
				return &thisConfig
			}(),
			expectedErr: errNoCChainGenesis,
		},
		"empty message": {
			networkID: 162,
			config: func() *Config {
				thisConfig := LocalFlareConfig
				thisConfig.Message = ""
				return &thisConfig
			}(),
			expectedErr: nil,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateConfig(test.networkID, test.config, genesisStakingCfg)
			require.ErrorIs(t, err, test.expectedErr)
		})
	}
}

func TestGenesisFromFile(t *testing.T) {
	tests := map[string]struct {
		networkID       uint32
		customConfig    []byte
		missingFilepath string
		expectedErr     error
		expectedHash    string
	}{
		"mainnet": {
			networkID:    constants.MainnetID,
			customConfig: customGenesisConfigJSON,
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"songbird": {
			networkID:    constants.SongbirdID,
			customConfig: customGenesisConfigJSON,
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"songbird (with custom specified)": {
			networkID:    constants.SongbirdID,
			customConfig: []byte(localGenesisConfigJSON), // won't load
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"local": {
			networkID:    constants.LocalID,
			customConfig: customGenesisConfigJSON,
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"local (with custom specified)": {
			networkID:    constants.LocalID,
			customConfig: customGenesisConfigJSON,
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"custom": {
			networkID:    9999,
			customConfig: customGenesisConfigJSON,
			expectedErr:  nil,
			expectedHash: "a1d1838586db85fe94ab1143560c3356df9ba2445794b796bba050be89f4fcb4",
		},
		"custom (networkID mismatch)": {
			networkID:    9999,
			customConfig: []byte(localGenesisConfigJSON),
			expectedErr:  errConflictingNetworkIDs,
		},
		"custom (invalid format)": {
			networkID:    9999,
			customConfig: invalidGenesisConfigJSON,
			expectedErr:  errInvalidGenesisJSON,
		},
		"custom (missing filepath)": {
			networkID:       9999,
			missingFilepath: "missing.json",
			expectedErr:     os.ErrNotExist,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require := require.New(t)

			// test loading of genesis from file
			var customFile string
			if len(test.customConfig) > 0 {
				customFile = filepath.Join(t.TempDir(), "config.json")
				require.NoError(perms.WriteFile(customFile, test.customConfig, perms.ReadWrite))
			}

			if len(test.missingFilepath) > 0 {
				customFile = test.missingFilepath
			}

			genesisBytes, _, err := FromFile(test.networkID, customFile, genesisStakingCfg)
			require.ErrorIs(err, test.expectedErr)
			if test.expectedErr == nil {
				genesisHash := hex.EncodeToString(hashing.ComputeHash256(genesisBytes))
				require.Equal(test.expectedHash, genesisHash, "genesis hash mismatch")

				_, err = genesis.Parse(genesisBytes)
				require.NoError(err)
			}
		})
	}
}

func TestGenesisFromFlag(t *testing.T) {
	tests := map[string]struct {
		networkID    uint32
		customConfig []byte
		expectedErr  error
		expectedHash string
	}{
		"mainnet": {
			networkID:   constants.MainnetID,
			expectedErr: errOverridesStandardNetworkConfig,
		},
		"songbird": {
			networkID: constants.SongbirdID,
			expectedErr: errOverridesStandardNetworkConfig,
		},
		"local": {
			networkID:   constants.LocalID,
			expectedErr: errOverridesStandardNetworkConfig,
		},
		"local (with custom specified)": {
			networkID:    constants.LocalID,
			customConfig: customGenesisConfigJSON,
			expectedErr:  errOverridesStandardNetworkConfig,
		},
		"custom": {
			networkID:    9999,
			customConfig: customGenesisConfigJSON,
			expectedErr:  nil,
			expectedHash: "a1d1838586db85fe94ab1143560c3356df9ba2445794b796bba050be89f4fcb4",
		},
		"custom (networkID mismatch)": {
			networkID:    9999,
			customConfig: []byte(localGenesisConfigJSON),
			expectedErr:  errConflictingNetworkIDs,
		},
		"custom (invalid format)": {
			networkID:    9999,
			customConfig: invalidGenesisConfigJSON,
			expectedErr:  errInvalidGenesisJSON,
		},
		"custom (missing content)": {
			networkID:   9999,
			expectedErr: errInvalidGenesisJSON,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require := require.New(t)

			// test loading of genesis content from flag/env-var
			var genBytes []byte
			if len(test.customConfig) == 0 {
				// try loading a default config
				var err error
				switch test.networkID {
				case constants.MainnetID:
					genBytes, err = json.Marshal(&MainnetConfig)
					require.NoError(err)
				case constants.TestnetID:
					genBytes, err = json.Marshal(&FujiConfig)
					require.NoError(err)
				case constants.LocalID:
					genBytes, err = json.Marshal(&LocalConfig)
					require.NoError(err)
				default:
					genBytes = make([]byte, 0)
				}
			} else {
				genBytes = test.customConfig
			}
			content := base64.StdEncoding.EncodeToString(genBytes)

			genesisBytes, _, err := FromFlag(test.networkID, content, genesisStakingCfg)
			require.ErrorIs(err, test.expectedErr)
			if test.expectedErr == nil {
				genesisHash := hex.EncodeToString(hashing.ComputeHash256(genesisBytes))
				require.Equal(test.expectedHash, genesisHash, "genesis hash mismatch")

				_, err = genesis.Parse(genesisBytes)
				require.NoError(err)
			}
		})
	}
}

func TestGenesis(t *testing.T) {
	tests := []struct {
		networkID  uint32
		expectedID string
	}{
		{
			networkID:  constants.MainnetID,
			expectedID: "UUvXi6j7QhVvgpbKM89MP5HdrxKm9CaJeHc187TsDNf8nZdLk",
		},
		{
			networkID:  constants.SongbirdID,
			expectedID: "wehzwSstW6ChWVj356tLr6sJfSb8PaZMhcerXUyxsAUzia1Gr",
		},
		{
			networkID:  constants.LocalID,
			expectedID: "pA6uxpovoxuKFwNxGndoX9YTYDUWCFnqwDodVWvS43UWT6Zde",
		},
	}
	for _, test := range tests {
		t.Run(constants.NetworkIDToNetworkName[test.networkID], func(t *testing.T) {
			require := require.New(t)

			config := GetConfig(test.networkID)
			genesisBytes, _, err := FromConfig(config)
			require.NoError(err)

			var genesisID ids.ID = hashing.ComputeHash256Array(genesisBytes)
			require.Equal(test.expectedID, genesisID.String())
		})
	}
}

func TestVMGenesis(t *testing.T) {
	type vmTest struct {
		vmID       ids.ID
		expectedID string
	}
	tests := []struct {
		networkID uint32
		vmTest    []vmTest
	}{
		{
			networkID: constants.MainnetID,
			vmTest: []vmTest{
				{
					vmID:       constants.AVMID,
					expectedID: "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM",
				},
				{
					vmID:       constants.EVMID,
					expectedID: "2q9e4r6Mu3U68nU1fYjgbR6JvwrRx36CohpAX5UQxse55x1Q5",
				},
			},
		},
		{
			networkID: constants.SongbirdID,
			vmTest: []vmTest{
				{
					vmID:       constants.AVMID,
					expectedID: "7xKYhEvYuUekwDxozgEiMPufzJ3jJPypKbGE8ny6KL84z4RKB",
				},
				{
					vmID:       constants.EVMID,
					expectedID: "2aamPVJ7SXz7DBUxFNY1Lyga52KWaVUH2Gz9Fetxo5gwjY5a29",
				},
			},
		},
		{
			networkID: constants.LocalID,
			vmTest: []vmTest{
				{
					vmID:       constants.AVMID,
					expectedID: "ALRkp1tuy7ErVkWuEWFLVd657JAULWDDyQkQBkLKVE94jCaNu",
				},
				{
					vmID:       constants.EVMID,
					expectedID: "yHEy62ti66aY6p4gzGWd2d5DCgSCuuYEnHJUagQVxPm24gz94",
				},
			},
		},
	}

	for _, test := range tests {
		for _, vmTest := range test.vmTest {
			name := fmt.Sprintf("%s-%s",
				constants.NetworkIDToNetworkName[test.networkID],
				vmTest.vmID,
			)
			t.Run(name, func(t *testing.T) {
				require := require.New(t)

				config := GetConfig(test.networkID)
				genesisBytes, _, err := FromConfig(config)
				require.NoError(err)

				genesisTx, err := VMGenesis(genesisBytes, vmTest.vmID)
				require.NoError(err)

				require.Equal(
					vmTest.expectedID,
					genesisTx.ID().String(),
					"%s genesisID with networkID %d mismatch",
					vmTest.vmID,
					test.networkID,
				)
			})
		}
	}
}

func TestAVAXAssetID(t *testing.T) {
	tests := []struct {
		networkID  uint32
		expectedID string
	}{
		{
			networkID:  constants.MainnetID,
			expectedID: "FvwEAhmxKfeiG8SnEvq42hc6whRyY3EFYAvebMqDNDGCgxN5Z",
		},
		{
			networkID:  constants.SongbirdID,
			expectedID: "1S3PSi4VsVpD8iK2vdykuajxVeuCV2xhjPSkQ4K88mqWGozMP",
		},
		{
			networkID:  constants.LocalID,
			expectedID: "2RULRJVXVpQNAsV3sBpy4G8LWH1LN3z5Adokv5bVtnZmsBQDCX",
		},
	}

	for _, test := range tests {
		t.Run(constants.NetworkIDToNetworkName[test.networkID], func(t *testing.T) {
			require := require.New(t)

			config := GetConfig(test.networkID)
			_, avaxAssetID, err := FromConfig(config)
			require.NoError(err)

			require.Equal(
				test.expectedID,
				avaxAssetID.String(),
				"AVAX assetID with networkID %d mismatch",
				test.networkID,
			)
		})
	}
}
