// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package constants

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/set"
)

// Const variables to be exported
const (
	MainnetID uint32 = 1
	CascadeID uint32 = 2
	DenaliID  uint32 = 3
	EverestID uint32 = 4
	FujiID    uint32 = 5

	TestnetID    uint32 = FujiID
	UnitTestID   uint32 = 10
	LocalID      uint32 = 12345
	FlareID      uint32 = 14
	CostwoID     uint32 = 114
	StagingID    uint32 = 161
	LocalFlareID uint32 = 162
	SongbirdID   uint32 = 5
	CostonID     uint32 = 7

	MainnetName    = "mainnet"
	CascadeName    = "cascade"
	DenaliName     = "denali"
	EverestName    = "everest"
	FujiName       = "fuji"
	TestnetName    = "testnet"
	UnitTestName   = "testing"
	LocalName      = "local"
	FlareName      = "flare"
	CostwoName     = "costwo"
	StagingName    = "staging"
	LocalFlareName = "localflare"
	SongbirdName   = "songbird"
	CostonName     = "coston"

	MainnetHRP    = "avax"
	CascadeHRP    = "cascade"
	DenaliHRP     = "denali"
	EverestHRP    = "everest"
	FujiHRP       = "fuji"
	UnitTestHRP   = "testing"
	LocalHRP      = "local"
	FallbackHRP   = "custom"
	FlareHRP      = "flare"
	CostwoHRP     = "costwo"
	StagingHRP    = "staging"
	LocalFlareHRP = "localflare"
	SongbirdHRP   = "songbird"
	CostonHRP     = "coston"
)

// Variables to be exported
var (
	PrimaryNetworkID = ids.Empty
	PlatformChainID  = ids.Empty

	NetworkIDToNetworkName = map[uint32]string{
		MainnetID: MainnetName,
		CascadeID: CascadeName,
		DenaliID:  DenaliName,
		EverestID: EverestName,
		// SGB-MERGE
		// FujiID:       FujiName,
		UnitTestID:   UnitTestName,
		LocalID:      LocalName,
		FlareID:      FlareName,
		CostwoID:     CostwoName,
		StagingID:    StagingName,
		LocalFlareID: LocalFlareName,
		SongbirdID:   SongbirdName,
		CostonID:     CostonName,
	}
	NetworkNameToNetworkID = map[string]uint32{
		MainnetName:    MainnetID,
		CascadeName:    CascadeID,
		DenaliName:     DenaliID,
		EverestName:    EverestID,
		FujiName:       FujiID,
		TestnetName:    TestnetID,
		UnitTestName:   UnitTestID,
		LocalName:      LocalID,
		FlareName:      FlareID,
		CostwoName:     CostwoID,
		StagingName:    StagingID,
		LocalFlareName: LocalFlareID,
		// SGB-MERGE
		SongbirdName: SongbirdID,
		CostonName:   CostonID,
	}

	NetworkIDToHRP = map[uint32]string{
		MainnetID:    MainnetHRP,
		CascadeID:    CascadeHRP,
		DenaliID:     DenaliHRP,
		EverestID:    EverestHRP,
		UnitTestID:   UnitTestHRP,
		LocalID:      LocalHRP,
		FlareID:      FlareHRP,
		CostwoID:     CostwoHRP,
		StagingID:    StagingHRP,
		LocalFlareID: LocalFlareHRP,
		// SGB-MERGE
		SongbirdID: SongbirdHRP,
		CostonID:   CostonHRP,
	}
	NetworkHRPToNetworkID = map[string]uint32{
		MainnetHRP:    MainnetID,
		CascadeHRP:    CascadeID,
		DenaliHRP:     DenaliID,
		EverestHRP:    EverestID,
		FujiHRP:       FujiID,
		UnitTestHRP:   UnitTestID,
		LocalHRP:      LocalID,
		FlareHRP:      FlareID,
		CostwoHRP:     CostwoID,
		StagingHRP:    StagingID,
		LocalFlareHRP: LocalFlareID,
		// SGB-MERGE
		SongbirdHRP: SongbirdID,
		CostonHRP:   CostonID,
	}
	ProductionNetworkIDs = set.Of(MainnetID, FujiID)

	ValidNetworkPrefix = "network-"

	ErrParseNetworkName = errors.New("failed to parse network name")
)

// GetHRP returns the Human-Readable-Part of bech32 addresses for a networkID
func GetHRP(networkID uint32) string {
	if hrp, ok := NetworkIDToHRP[networkID]; ok {
		return hrp
	}
	return FallbackHRP
}

// NetworkName returns a human readable name for the network with
// ID [networkID]
func NetworkName(networkID uint32) string {
	if name, exists := NetworkIDToNetworkName[networkID]; exists {
		return name
	}
	return fmt.Sprintf("network-%d", networkID)
}

// NetworkID returns the ID of the network with name [networkName]
func NetworkID(networkName string) (uint32, error) {
	networkName = strings.ToLower(networkName)
	if id, exists := NetworkNameToNetworkID[networkName]; exists {
		return id, nil
	}

	idStr := networkName
	if strings.HasPrefix(networkName, ValidNetworkPrefix) {
		idStr = networkName[len(ValidNetworkPrefix):]
	}
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrParseNetworkName, networkName)
	}
	return uint32(id), nil
}
