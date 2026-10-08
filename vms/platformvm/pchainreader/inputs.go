// (c) 2026, Flare Network. All rights reserved.
// See the file LICENSE for licensing terms.

package pchainreader

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/pchain"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/components/verify"
	"github.com/ava-labs/avalanchego/vms/platformvm/stakeable"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
)

// utxoKey identifies a consumed output.
type utxoKey struct {
	txID  ids.ID
	index uint32
}

// ownerEntry is the resolved owner of a consumed output.
type ownerEntry struct {
	resolved          bool
	genesis           bool
	locktime          uint64
	stakeableLocktime uint64
	threshold         uint32
	addrs             []ids.ShortID
	// txBytes is the stored size of the producing transaction, charged on
	// every resolution of this output (0 for genesis UTXOs).
	txBytes uint64
}

func (b *blockReader) GetInputs(
	txIndex, from, max uint32,
	order pchain.InputOrder,
	charge pchain.ByteCharger,
) ([]pchain.Input, uint32, error) {
	if order > pchain.InputOrderByUTXOTxIDString {
		return nil, 0, fmt.Errorf("%w: %d", pchain.ErrInvalidInputOrder, order)
	}

	r, db := b.r, b.db
	if uint64(txIndex) >= uint64(len(db.block.Txs)) {
		return nil, 0, fmt.Errorf("%w: index %d, block has %d transactions", pchain.ErrTxIndexOutOfRange, txIndex, len(db.block.Txs))
	}
	if db.block.Txs[txIndex].Type == pchain.TxUnsupported {
		return nil, 0, fmt.Errorf("%w: transaction %d", pchain.ErrUnsupportedTx, txIndex)
	}

	ins := db.ins[txIndex]
	total := uint32(len(ins))
	if from > total {
		return nil, total, fmt.Errorf("%w: from %d, transaction has %d inputs", pchain.ErrInputIndexOutOfRange, from, total)
	}
	end := total
	if max < total-from {
		end = from + max
	}
	inputs := make([]pchain.Input, 0, end-from)
	if from == end {
		return inputs, total, nil // an empty page orders and resolves nothing
	}

	// positions holds the canonical positions of the page in the requested
	// order; nil for the canonical order itself.
	var positions []int
	if order == pchain.InputOrderByUTXOTxIDString {
		positions = db.inputsByString(txIndex)[from:end]
	}
	importedFrom := int(db.importedFrom[txIndex])
	for k := from; k < end; k++ {
		position := int(k)
		if positions != nil {
			position = positions[k-from]
		}
		input, err := r.resolveInput(ins[position], db.creds[txIndex][position], position >= importedFrom, charge)
		if err != nil {
			return nil, total, err
		}
		inputs = append(inputs, input)
	}
	return inputs, total, nil
}

// permutationByString returns the indexes of ins sorted by the CB58 string of
// their UTXO transaction ID, then by output index. It converts every input's
// ID and sorts them: O(n log n) in the number of inputs.
func permutationByString(ins []*avax.TransferableInput) []int {
	perm := make([]int, len(ins))
	for i := range perm {
		perm[i] = i
	}

	keys := make([]string, len(ins))
	for i, in := range ins {
		keys[i] = in.TxID.String()
	}
	sort.SliceStable(perm, func(a, b int) bool {
		ia, ib := perm[a], perm[b]
		if keys[ia] != keys[ib] {
			return keys[ia] < keys[ib]
		}
		return ins[ia].OutputIndex < ins[ib].OutputIndex
	})
	return perm
}

// resolveInput decodes a consumed input together with the signatures of its
// credential cred (nil when the transaction has none for it) and, unless it
// is an imported input, resolves the owner of the consumed output.
func (r *Reader) resolveInput(in *avax.TransferableInput, cred *secp256k1fx.Credential, imported bool, charge pchain.ByteCharger) (pchain.Input, error) {
	// The asset an accepted input claims is the asset of the output it
	// consumes (VerifySpendUTXOs checks it, for imported inputs against the
	// shared-memory UTXO), so it is known without resolving the output.
	input := pchain.Input{
		UTXOTxID:        in.TxID,
		UTXOOutputIndex: in.OutputIndex,
		AssetID:         in.AssetID(),
		Imported:        imported,
	}

	transferIn := in.In
	if locked, ok := transferIn.(*stakeable.LockIn); ok {
		transferIn = locked.TransferableIn
	}
	if secpIn, ok := transferIn.(*secp256k1fx.TransferInput); ok {
		input.Amount = secpIn.Amt
		input.SigIndices = secpIn.SigIndices
	} else if transferIn != nil {
		input.Amount = transferIn.Amount()
	}
	if cred != nil {
		input.Signatures = cred.Sigs
	}
	if imported {
		// The consumed UTXO was produced on the source chain: there is no
		// producing P-chain transaction and no genesis entry to look up (an
		// imported UTXO ID may even collide with a genesis key).
		return input, nil
	}

	owner, err := r.resolveOwner(utxoKey{txID: in.TxID, index: in.OutputIndex}, charge)
	if err != nil {
		return pchain.Input{}, err
	}
	input.Genesis = owner.genesis
	if owner.resolved {
		input.Resolved = true
		input.Locktime = owner.locktime
		input.StakeableLocktime = owner.stakeableLocktime
		input.Threshold = owner.threshold
		input.Addresses = owner.addrs
	}
	return input, nil
}

// resolveOwner locates the output consumed by key and returns its owner.
// Genesis UTXOs are looked up in the genesis allocation (they have no
// producing transaction). Otherwise the producing transaction's stored size
// is charged, on cache hits too, before the transaction is parsed outside the
// P-chain lock. A UTXO produced outside the known rules (BaseTx outputs,
// returned stake, and reward UTXOs) resolves to an unresolved entry, never to
// an error; a missing producing transaction is a local database problem and
// is an error.
func (r *Reader) resolveOwner(key utxoKey, charge pchain.ByteCharger) (ownerEntry, error) {
	if entry, ok := r.owners.Get(key); ok {
		// Genesis UTXOs (txBytes 0) charge nothing on either path.
		if entry.txBytes > 0 {
			if err := chargeBytes(charge, entry.txBytes); err != nil {
				return ownerEntry{}, err
			}
		}
		return entry, nil
	}

	var entry ownerEntry
	if utxo, ok := r.genesisUTXO(key); ok {
		entry = ownersFromOutput(utxo.Out)
		entry.genesis = true
	} else {
		txBytes, err := r.store.GetTxBytes(key.txID)
		if err != nil {
			return ownerEntry{}, fmt.Errorf("fetching producing P-chain tx %s: %w", key.txID, err)
		}
		size := uint64(len(txBytes))
		if err := chargeBytes(charge, size); err != nil {
			return ownerEntry{}, err
		}
		tx, err := txs.Parse(txs.GenesisCodec, txBytes)
		if err != nil {
			return ownerEntry{}, fmt.Errorf("parsing producing P-chain tx %s: %w", key.txID, err)
		}
		out, err := r.producedOutput(tx, key)
		if err != nil {
			return ownerEntry{}, err
		}
		entry = ownersFromOutput(out)
		entry.txBytes = size
	}

	r.owners.Put(key, entry)
	return entry, nil
}

// producedOutput returns the output at key.index of the producing transaction
// tx, following the P-chain's UTXO index assignment: BaseTx outputs first,
// then a staker's returned stake (avax.Produce / unstakeUTXOs), then reward
// UTXOs (state.AddRewardUTXO). It returns nil when no such output is known.
func (r *Reader) producedOutput(tx *txs.Tx, key utxoKey) (verify.State, error) {
	index := int(key.index)
	outs := tx.Unsigned.Outputs()
	if index < len(outs) {
		return outs[index].Out, nil
	}
	if staker, ok := tx.Unsigned.(txs.PermissionlessStaker); ok {
		stake := staker.Stake()
		if j := index - len(outs); j < len(stake) {
			return stake[j].Out, nil
		}
	}

	utxos, err := r.store.GetRewardUTXOs(key.txID)
	if err != nil && !errors.Is(err, database.ErrNotFound) {
		return nil, fmt.Errorf("fetching reward UTXOs of P-chain tx %s: %w", key.txID, err)
	}
	for _, utxo := range utxos {
		if utxo.OutputIndex == key.index {
			return utxo.Out, nil
		}
	}
	return nil, nil
}

// secpOwner unwraps a stakeable lock around out and returns the lock's
// locktime (0 when unlocked) and the secp256k1fx transfer output, or false
// for an output of any other type.
func secpOwner(out verify.State) (uint64, *secp256k1fx.TransferOutput, bool) {
	var stakeableLocktime uint64
	if locked, ok := out.(*stakeable.LockOut); ok {
		stakeableLocktime = locked.Locktime
		out = locked.TransferableOut
	}
	transferOut, ok := out.(*secp256k1fx.TransferOutput)
	return stakeableLocktime, transferOut, ok
}

// ownersFromOutput extracts the secp256k1fx owner of out, unwrapping a
// stakeable lock. Outputs of other types are unresolved.
func ownersFromOutput(out verify.State) ownerEntry {
	stakeableLocktime, transferOut, ok := secpOwner(out)
	if !ok {
		return ownerEntry{}
	}
	return ownerEntry{
		resolved:          true,
		locktime:          transferOut.Locktime,
		stakeableLocktime: stakeableLocktime,
		threshold:         transferOut.Threshold,
		addrs:             transferOut.Addrs,
	}
}
