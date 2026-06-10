// Copyright (C) 2019-2025, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package customrawdb

import (
	"errors"
	"slices"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/rawdb"
	"github.com/ava-labs/libevm/ethdb"
	"github.com/stretchr/testify/require"
)

func TestClearPrefix(t *testing.T) {
	require := require.New(t)
	db := rawdb.NewMemoryDatabase()
	// add a key that should be cleared
	require.NoError(WriteSyncSegment(db, common.Hash{1}, common.Hash{}))

	// add a key that should not be cleared
	key := slices.Concat(syncSegmentsPrefix, []byte("foo"))
	require.NoError(db.Put(key, []byte("bar")))

	require.NoError(ClearAllSyncSegments(db))

	count := 0
	it := db.NewIterator(syncSegmentsPrefix, nil)
	defer it.Release()
	for it.Next() {
		count++
	}
	require.NoError(it.Error())
	require.Equal(1, count)
}

func TestGetLatestSyncPerformed(t *testing.T) {
	require := require.New(t)
	db := rawdb.NewMemoryDatabase()

	require.NoError(WriteSyncPerformed(db, 10))
	require.NoError(WriteSyncPerformed(db, 20))
	require.NoError(WriteSyncPerformed(db, 15))

	latest, err := GetLatestSyncPerformed(db)
	require.NoError(err)
	require.Equal(uint64(20), latest)
}

func TestGetLatestSyncPerformedEmpty(t *testing.T) {
	require := require.New(t)
	db := rawdb.NewMemoryDatabase()

	latest, err := GetLatestSyncPerformed(db)
	require.NoError(err)
	require.Zero(latest)
}

func TestGetLatestSyncPerformedIteratorError(t *testing.T) {
	expectedErr := errors.New("iterator failed")

	latest, err := GetLatestSyncPerformed(errorIteratee{
		err: expectedErr,
	})
	require.ErrorIs(t, err, expectedErr)
	require.Zero(t, latest)
}

type errorIteratee struct {
	err error
}

func (e errorIteratee) NewIterator(_, _ []byte) ethdb.Iterator {
	return errorIterator{err: e.err}
}

type errorIterator struct {
	err error
}

func (errorIterator) Next() bool {
	return false
}

func (e errorIterator) Error() error {
	return e.err
}

func (errorIterator) Key() []byte {
	return nil
}

func (errorIterator) Value() []byte {
	return nil
}

func (errorIterator) Release() {}
