// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/consensus/snowman"
	"github.com/ava-labs/avalanchego/snow/engine/snowman/bootstrap/interval"
)

var (
	_ interval.Parser = (*parseAcceptor)(nil)
	_ snowman.Block   = (*blockAcceptor)(nil)
)

type parseAcceptor struct {
	parser      interval.Parser
	ctx         *snow.ConsensusContext
	numAccepted prometheus.Counter
}

func (p *parseAcceptor) ParseBlock(ctx context.Context, bytes []byte) (snowman.Block, error) {
	blk, err := p.parser.ParseBlock(ctx, bytes)
	if err != nil {
		return nil, err
	}
	return &blockAcceptor{
		Block:       blk,
		ctx:         p.ctx,
		numAccepted: p.numAccepted,
	}, nil
}

type blockAcceptor struct {
	snowman.Block

	ctx         *snow.ConsensusContext
	numAccepted prometheus.Counter
}

func (b *blockAcceptor) Accept(ctx context.Context) error {
	b.numAccepted.Inc()
	if err := b.ctx.BlockAcceptor.Accept(b.ctx, b.ID(), b.Bytes()); err != nil {
		return err
	}
	return b.Block.Accept(ctx)
}
