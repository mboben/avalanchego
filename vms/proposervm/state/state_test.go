// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package state

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/database/memdb"
	"github.com/ava-labs/avalanchego/database/versiondb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/vms/proposervm/block"
)

func TestState(t *testing.T) {
	a := require.New(t)

	db := memdb.New()
	vdb := versiondb.New(db)
	s, err := New(vdb)
	a.NoError(err)

	testBlockState(a, s)
	testChainState(a, s)
}

func TestMeteredState(t *testing.T) {
	a := require.New(t)

	db := memdb.New()
	vdb := versiondb.New(db)
	s, err := NewMetered(vdb, "", prometheus.NewRegistry())
	a.NoError(err)

	testBlockState(a, s)
	testChainState(a, s)
}

// We should delete any pending verified blocks on startup, in the event that
// a block which was previously verified issued to consensus never had
// accept/reject called on it (e.g. if went offline).
func TestPruneVerifiedBlocksOnRestart(t *testing.T) {
	require := require.New(t)

	db := versiondb.New(memdb.New())
	state, err := New(db)
	require.NoError(err)

	blk0, err := block.BuildUnsigned(ids.Empty, time.Time{}, 0, []byte("block 0"))
	require.NoError(err)

	blk1, err := block.BuildUnsigned(blk0.ID(), time.Time{}, 0, []byte("block 0"))
	require.NoError(err)

	blk2, err := block.BuildUnsigned(blk1.ID(), time.Time{}, 0, []byte("block 0"))
	require.NoError(err)

	require.NoError(state.PutVerifiedBlock(blk0.ID()))
	require.NoError(state.PutBlock(blk0, choices.Processing))

	require.NoError(state.PutVerifiedBlock(blk1.ID()))
	require.NoError(state.PutBlock(blk1, choices.Processing))

	require.NoError(state.PutVerifiedBlock(blk2.ID()))
	require.NoError(state.PutBlock(blk2, choices.Accepted))
	require.NoError(state.SetPreference(blk2.ID()))

	ok, err := state.HasVerifiedBlock(blk0.ID())
	require.NoError(err)
	require.True(ok)
	ok, err = state.HasVerifiedBlock(blk1.ID())
	require.NoError(err)
	require.True(ok)
	ok, err = state.HasVerifiedBlock(blk2.ID())
	require.NoError(err)
	require.True(ok)

	require.NoError(db.Commit())

	// we should prune the previously pending blocks upon restart
	state, err = New(db)
	require.NoError(err)

	ok, err = state.HasVerifiedBlock(blk0.ID())
	require.NoError(err)
	require.False(ok)

	ok, err = state.HasVerifiedBlock(blk1.ID())
	require.NoError(err)
	require.False(ok)

	ok, err = state.HasVerifiedBlock(blk2.ID())
	require.NoError(err)
	require.False(ok)
}
