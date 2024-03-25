// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/engine/snowman/block"
	"github.com/ava-labs/avalanchego/snow/engine/snowman/bootstrap/interval"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/utils/timer"
)

const (
	batchWritePeriod      = 64
	iteratorReleasePeriod = 1024
	logPeriod             = 5 * time.Second
)

// getMissingBlockIDs returns the ID of the blocks that should be fetched to
// attempt to make a single continuous range from
// (lastAcceptedHeight, highestTrackedHeight].
//
// For example, if the tree currently contains heights [1, 4, 6, 7] and the
// lastAcceptedHeight is 2, this function will return the IDs corresponding to
// blocks [3, 5].
func getMissingBlockIDs(
	ctx context.Context,
	db database.KeyValueReader,
	parser block.Parser,
	tree *interval.Tree,
	lastAcceptedHeight uint64,
) (set.Set[ids.ID], error) {
	var (
		missingBlocks     set.Set[ids.ID]
		intervals         = tree.Flatten()
		lastHeightToFetch = lastAcceptedHeight + 1
	)
	for _, i := range intervals {
		if i.LowerBound <= lastHeightToFetch {
			continue
		}

		blkBytes, err := interval.GetBlock(db, i.LowerBound)
		if err != nil {
			return nil, err
		}

		blk, err := parser.ParseBlock(ctx, blkBytes)
		if err != nil {
			return nil, err
		}

		parentID := blk.Parent()
		missingBlocks.Add(parentID)
	}
	return missingBlocks, nil
}

// execute all the blocks tracked by the tree. If a block is in the tree but is
// already accepted based on the lastAcceptedHeight, it will be removed from the
// tree but not executed.
//
// execute assumes that getMissingBlockIDs would return an empty set.
func execute(
	ctx context.Context,
	log logging.Func,
	db database.Database,
	parser block.Parser,
	tree *interval.Tree,
	lastAcceptedHeight uint64,
) error {
	var (
		batch                    = db.NewBatch()
		processedSinceBatchWrite uint
		writeBatch               = func() error {
			if processedSinceBatchWrite == 0 {
				return nil
			}
			processedSinceBatchWrite = 0

			if err := batch.Write(); err != nil {
				return err
			}
			batch.Reset()
			return nil
		}

		iterator                      = interval.GetBlockIterator(db)
		processedSinceIteratorRelease uint

		startTime            = time.Now()
		timeOfNextLog        = startTime.Add(logPeriod)
		totalNumberToProcess = tree.Len()
	)
	defer func() {
		iterator.Release()
	}()

	log("executing blocks",
		zap.Uint64("numToExecute", totalNumberToProcess),
	)

	for ctx.Err() == nil && iterator.Next() {
		blkBytes := iterator.Value()
		blk, err := parser.ParseBlock(ctx, blkBytes)
		if err != nil {
			return err
		}

		height := blk.Height()
		if err := interval.Remove(batch, tree, height); err != nil {
			return err
		}

		// Periodically write the batch to disk to avoid memory pressure.
		processedSinceBatchWrite++
		if processedSinceBatchWrite >= batchWritePeriod {
			if err := writeBatch(); err != nil {
				return err
			}
		}

		// Periodically release and re-grab the database iterator to avoid
		// keeping a reference to an old database revision.
		processedSinceIteratorRelease++
		if processedSinceIteratorRelease >= iteratorReleasePeriod {
			if err := iterator.Error(); err != nil {
				return err
			}

			// The batch must be written here to avoid re-processing a block.
			if err := writeBatch(); err != nil {
				return err
			}

			processedSinceIteratorRelease = 0
			iterator.Release()
			iterator = interval.GetBlockIterator(db)
		}

		now := time.Now()
		if now.After(timeOfNextLog) {
			var (
				numProcessed = totalNumberToProcess - tree.Len()
				eta          = timer.EstimateETA(startTime, numProcessed, totalNumberToProcess)
			)

			log("executing blocks",
				zap.Duration("eta", eta),
				zap.Uint64("numExecuted", numProcessed),
				zap.Uint64("numToExecute", totalNumberToProcess),
			)
			timeOfNextLog = now.Add(logPeriod)
		}

		if height <= lastAcceptedHeight {
			continue
		}

		if err := blk.Verify(ctx); err != nil {
			return fmt.Errorf("failed to verify block %s (%d) in bootstrapping: %w",
				blk.ID(),
				height,
				err,
			)
		}
		if err := blk.Accept(ctx); err != nil {
			return fmt.Errorf("failed to accept block %s (%d) in bootstrapping: %w",
				blk.ID(),
				height,
				err,
			)
		}
	}
	if err := writeBatch(); err != nil {
		return err
	}
	if err := iterator.Error(); err != nil {
		return err
	}

	var (
		numProcessed = totalNumberToProcess - tree.Len()
		err          = ctx.Err()
	)
	log("executed blocks",
		zap.Uint64("numExecuted", numProcessed),
		zap.Uint64("numToExecute", totalNumberToProcess),
		zap.Duration("duration", time.Since(startTime)),
		zap.Error(err),
	)
	return err
}
