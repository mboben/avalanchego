// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchainreader

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/block"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/vms/types"
)

// Calibration benchmarks for the P-chain gateway's per-byte gas: they measure
// how fast block.Parse and the reader's decode run on typical and pathological
// blocks (bytes/op is set, so the result reports MB/s).

func benchBlock(b *testing.B, blkTxs []*txs.Tx) []byte {
	b.Helper()
	blk, err := block.NewBanffStandardBlock(time.Unix(postDurangoTime, 0), ids.GenerateTestID(), 1, blkTxs)
	require.NoError(b, err)
	return blk.Bytes()
}

func manyStakingTxs(b *testing.B, n int) []*txs.Tx {
	b.Helper()
	out := make([]*txs.Tx, n)
	for i := range out {
		ins := []*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 1000, 0), transferInput(ids.GenerateTestID(), 1, 1, 0)}
		tx, err := txs.NewSigned(permissionlessValidator(constants.PrimaryNetworkID, ids.GenerateTestNodeID(), ins), txs.Codec, nil)
		require.NoError(b, err)
		out[i] = tx
	}
	return out
}

func wideOutputTx(b *testing.B, nOuts int) *txs.Tx {
	b.Helper()
	outs := make([]*avax.TransferableOutput, nOuts)
	for i := range outs {
		outs[i] = transferOutput(1, owners(ids.ShortID{byte(i), byte(i >> 8)}))
	}
	tx, err := txs.NewSigned(&txs.BaseTx{BaseTx: avax.BaseTx{
		NetworkID:    constants.UnitTestID,
		BlockchainID: constants.PlatformChainID,
		Ins:          []*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 1, 0)},
		Outs:         outs,
		Memo:         types.JSONByteSlice{},
	}}, txs.Codec, nil)
	require.NoError(b, err)
	return tx
}

func manyAddressTx(b *testing.B, nAddrs int) *txs.Tx {
	b.Helper()
	addrs := make([]ids.ShortID, nAddrs)
	for i := range addrs {
		addrs[i] = ids.ShortID{byte(i), byte(i >> 8), byte(i >> 16)}
	}
	tx, err := txs.NewSigned(&txs.BaseTx{BaseTx: avax.BaseTx{
		NetworkID:    constants.UnitTestID,
		BlockchainID: constants.PlatformChainID,
		Ins:          []*avax.TransferableInput{transferInput(ids.GenerateTestID(), 0, 1, 0)},
		Outs: []*avax.TransferableOutput{{
			Asset: avax.Asset{ID: avaxAssetID},
			Out:   &secp256k1fx.TransferOutput{Amt: 1, OutputOwners: secp256k1fx.OutputOwners{Threshold: 1, Addrs: addrs}},
		}},
		Memo: types.JSONByteSlice{},
	}}, txs.Codec, nil)
	require.NoError(b, err)
	return tx
}

func runParseBench(b *testing.B, bytes []byte) {
	b.Helper()
	reader := New(newFakeStore(), nil, testUpgrades)
	b.SetBytes(int64(len(bytes)))
	b.ReportMetric(float64(len(bytes)), "block_bytes")
	b.ResetTimer()
	for range b.N {
		blk, err := block.Parse(block.GenesisCodec, bytes)
		if err != nil {
			b.Fatal(err)
		}
		reader.decode(blk)
	}
}

func BenchmarkParseTypicalBlock(b *testing.B) { runParseBench(b, benchBlock(b, manyStakingTxs(b, 20))) }
func BenchmarkParseLargeStakingBlock(b *testing.B) {
	runParseBench(b, benchBlock(b, manyStakingTxs(b, 2000)))
}
func BenchmarkParseWideOutputBlock(b *testing.B) {
	runParseBench(b, benchBlock(b, []*txs.Tx{wideOutputTx(b, 12000)}))
}
func BenchmarkParseManyAddressBlock(b *testing.B) {
	runParseBench(b, benchBlock(b, []*txs.Tx{manyAddressTx(b, 50000)}))
}

// BenchmarkPermutationByString calibrates the gateway's per-input ordering
// gas: reading inputs in CB58-string order converts every input's UTXO
// transaction ID to its string and sorts them, whatever the page size.
func BenchmarkPermutationByString(b *testing.B) {
	for _, n := range []int{16, 1_000, 20_000} {
		ins := make([]*avax.TransferableInput, n)
		for i := range ins {
			ins[i] = transferInput(ids.GenerateTestID(), uint32(i), 1, 0)
		}
		b.Run(fmt.Sprintf("inputs=%d", n), func(b *testing.B) {
			for b.Loop() {
				permutationByString(ins)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/input")
		})
	}
}
