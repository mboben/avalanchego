// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package p

import (
	"errors"
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/signer"
	"github.com/ava-labs/avalanchego/vms/platformvm/status"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/fees"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/subnet/primary/common"

	commonfees "github.com/ava-labs/avalanchego/vms/components/fees"
)

var (
	errNotCommitted = errors.New("not committed")

	_ Wallet = (*wallet)(nil)
)

type Wallet interface {
	Context

	// Builder returns the builder that will be used to create the transactions.
	Builder() Builder

	// Signer returns the signer that will be used to sign the transactions.
	Signer() Signer

	// IssueBaseTx creates, signs, and issues a new simple value transfer.
	// Because the P-chain doesn't intend for balance transfers to occur, this
	// method is expensive and abuses the creation of subnets.
	//
	// - [outputs] specifies all the recipients and amounts that should be sent
	//   from this transaction.
	IssueBaseTx(
		outputs []*avax.TransferableOutput,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddValidatorTx creates, signs, and issues a new validator of the
	// primary network.
	//
	// - [vdr] specifies all the details of the validation period such as the
	//   startTime, endTime, stake weight, and nodeID.
	// - [rewardsOwner] specifies the owner of all the rewards this validator
	//   may accrue during its validation period.
	// - [shares] specifies the fraction (out of 1,000,000) that this validator
	//   will take from delegation rewards. If 1,000,000 is provided, 100% of
	//   the delegation reward will be sent to the validator's [rewardsOwner].
	IssueAddValidatorTx(
		vdr *txs.Validator,
		rewardsOwner *secp256k1fx.OutputOwners,
		shares uint32,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddSubnetValidatorTx creates, signs, and issues a new validator of a
	// subnet.
	//
	// - [vdr] specifies all the details of the validation period such as the
	//   startTime, endTime, sampling weight, nodeID, and subnetID.
	IssueAddSubnetValidatorTx(
		vdr *txs.SubnetValidator,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddSubnetValidatorTx creates, signs, and issues a transaction that
	// removes a validator of a subnet.
	//
	// - [nodeID] is the validator being removed from [subnetID].
	IssueRemoveSubnetValidatorTx(
		nodeID ids.NodeID,
		subnetID ids.ID,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddDelegatorTx creates, signs, and issues a new delegator to a
	// validator on the primary network.
	//
	// - [vdr] specifies all the details of the delegation period such as the
	//   startTime, endTime, stake weight, and validator's nodeID.
	// - [rewardsOwner] specifies the owner of all the rewards this delegator
	//   may accrue at the end of its delegation period.
	IssueAddDelegatorTx(
		vdr *txs.Validator,
		rewardsOwner *secp256k1fx.OutputOwners,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueCreateChainTx creates, signs, and issues a new chain in the named
	// subnet.
	//
	// - [subnetID] specifies the subnet to launch the chain in.
	// - [genesis] specifies the initial state of the new chain.
	// - [vmID] specifies the vm that the new chain will run.
	// - [fxIDs] specifies all the feature extensions that the vm should be
	//   running with.
	// - [chainName] specifies a human readable name for the chain.
	IssueCreateChainTx(
		subnetID ids.ID,
		genesis []byte,
		vmID ids.ID,
		fxIDs []ids.ID,
		chainName string,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueCreateSubnetTx creates, signs, and issues a new subnet with the
	// specified owner.
	//
	// - [owner] specifies who has the ability to create new chains and add new
	//   validators to the subnet.
	IssueCreateSubnetTx(
		owner *secp256k1fx.OutputOwners,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueTransferSubnetOwnershipTx creates, signs, and issues a transaction that
	// changes the owner of the named subnet.
	//
	// - [subnetID] specifies the subnet to be modified
	// - [owner] specifies who has the ability to create new chains and add new
	//   validators to the subnet.
	IssueTransferSubnetOwnershipTx(
		subnetID ids.ID,
		owner *secp256k1fx.OutputOwners,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueImportTx creates, signs, and issues an import transaction that
	// attempts to consume all the available UTXOs and import the funds to [to].
	//
	// - [chainID] specifies the chain to be importing funds from.
	// - [to] specifies where to send the imported funds to.
	IssueImportTx(
		chainID ids.ID,
		to *secp256k1fx.OutputOwners,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueExportTx creates, signs, and issues an export transaction that
	// attempts to send all the provided [outputs] to the requested [chainID].
	//
	// - [chainID] specifies the chain to be exporting the funds to.
	// - [outputs] specifies the outputs to send to the [chainID].
	IssueExportTx(
		chainID ids.ID,
		outputs []*avax.TransferableOutput,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueTransformSubnetTx creates a transform subnet transaction that attempts
	// to convert the provided [subnetID] from a permissioned subnet to a
	// permissionless subnet. This transaction will convert
	// [maxSupply] - [initialSupply] of [assetID] to staking rewards.
	//
	// - [subnetID] specifies the subnet to transform.
	// - [assetID] specifies the asset to use to reward stakers on the subnet.
	// - [initialSupply] is the amount of [assetID] that will be in circulation
	//   after this transaction is accepted.
	// - [maxSupply] is the maximum total amount of [assetID] that should ever
	//   exist.
	// - [minConsumptionRate] is the rate that a staker will receive rewards
	//   if they stake with a duration of 0.
	// - [maxConsumptionRate] is the maximum rate that staking rewards should be
	//   consumed from the reward pool per year.
	// - [minValidatorStake] is the minimum amount of funds required to become a
	//   validator.
	// - [maxValidatorStake] is the maximum amount of funds a single validator
	//   can be allocated, including delegated funds.
	// - [minStakeDuration] is the minimum number of seconds a staker can stake
	//   for.
	// - [maxStakeDuration] is the maximum number of seconds a staker can stake
	//   for.
	// - [minValidatorStake] is the minimum amount of funds required to become a
	//   delegator.
	// - [maxValidatorWeightFactor] is the factor which calculates the maximum
	//   amount of delegation a validator can receive. A value of 1 effectively
	//   disables delegation.
	// - [uptimeRequirement] is the minimum percentage a validator must be
	//   online and responsive to receive a reward.
	IssueTransformSubnetTx(
		subnetID ids.ID,
		assetID ids.ID,
		initialSupply uint64,
		maxSupply uint64,
		minConsumptionRate uint64,
		maxConsumptionRate uint64,
		minValidatorStake uint64,
		maxValidatorStake uint64,
		minStakeDuration time.Duration,
		maxStakeDuration time.Duration,
		minDelegationFee uint32,
		minDelegatorStake uint64,
		maxValidatorWeightFactor byte,
		uptimeRequirement uint32,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddPermissionlessValidatorTx creates, signs, and issues a new
	// validator of the specified subnet.
	//
	// - [vdr] specifies all the details of the validation period such as the
	//   subnetID, startTime, endTime, stake weight, and nodeID.
	// - [signer] if the subnetID is the primary network, this is the BLS key
	//   for this validator. Otherwise, this value should be the empty signer.
	// - [assetID] specifies the asset to stake.
	// - [validationRewardsOwner] specifies the owner of all the rewards this
	//   validator earns for its validation period.
	// - [delegationRewardsOwner] specifies the owner of all the rewards this
	//   validator earns for delegations during its validation period.
	// - [shares] specifies the fraction (out of 1,000,000) that this validator
	//   will take from delegation rewards. If 1,000,000 is provided, 100% of
	//   the delegation reward will be sent to the validator's [rewardsOwner].
	IssueAddPermissionlessValidatorTx(
		vdr *txs.SubnetValidator,
		signer signer.Signer,
		assetID ids.ID,
		validationRewardsOwner *secp256k1fx.OutputOwners,
		delegationRewardsOwner *secp256k1fx.OutputOwners,
		shares uint32,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueAddPermissionlessDelegatorTx creates, signs, and issues a new
	// delegator of the specified subnet on the specified nodeID.
	//
	// - [vdr] specifies all the details of the delegation period such as the
	//   subnetID, startTime, endTime, stake weight, and nodeID.
	// - [assetID] specifies the asset to stake.
	// - [rewardsOwner] specifies the owner of all the rewards this delegator
	//   earns during its delegation period.
	IssueAddPermissionlessDelegatorTx(
		vdr *txs.SubnetValidator,
		assetID ids.ID,
		rewardsOwner *secp256k1fx.OutputOwners,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueUnsignedTx signs and issues the unsigned tx.
	IssueUnsignedTx(
		utx txs.UnsignedTx,
		options ...common.Option,
	) (*txs.Tx, error)

	// IssueTx issues the signed tx.
	IssueTx(
		tx *txs.Tx,
		options ...common.Option,
	) error
}

func NewWallet(
	builder Builder,
	dynFeesBuilder *DynamicFeesBuilder,
	signer Signer,
	client platformvm.Client,
	backend Backend,
) Wallet {
	return &wallet{
		Backend:        backend,
		builder:        builder,
		dynamicBuilder: dynFeesBuilder,
		signer:         signer,
		client:         client,
	}
}

type wallet struct {
	Backend
	signer Signer
	client platformvm.Client

	isEForkActive      bool
	builder            Builder
	dynamicBuilder     *DynamicFeesBuilder
	unitFees, unitCaps commonfees.Dimensions
}

func (w *wallet) Builder() Builder {
	return w.builder
}

func (w *wallet) Signer() Signer {
	return w.signer
}

func (w *wallet) IssueBaseTx(
	outputs []*avax.TransferableOutput,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	var (
		utx txs.UnsignedTx
		err error

		feesMan = commonfees.NewManager(w.unitFees)
		feeCalc = &fees.Calculator{
			IsEUpgradeActive: w.isEForkActive,
			Config: &config.Config{
				CreateSubnetTxFee: w.CreateSubnetTxFee(),
			},
			FeeManager:       feesMan,
			ConsumedUnitsCap: w.unitCaps,
		}
	)
	if w.isEForkActive {
		utx, err = w.dynamicBuilder.NewBaseTx(outputs, feeCalc, options...)
	} else {
		utx, err = w.dynamicBuilder.newBaseTxPreEUpgrade(outputs, feeCalc, options...)
	}
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueAddValidatorTx(
	vdr *txs.Validator,
	rewardsOwner *secp256k1fx.OutputOwners,
	shares uint32,
	options ...common.Option,
) (*txs.Tx, error) {
	utx, err := w.builder.NewAddValidatorTx(vdr, rewardsOwner, shares, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueAddSubnetValidatorTx(
	vdr *txs.SubnetValidator,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TxFee: w.BaseTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewAddSubnetValidatorTx(vdr, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueRemoveSubnetValidatorTx(
	nodeID ids.NodeID,
	subnetID ids.ID,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TxFee: w.BaseTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewRemoveSubnetValidatorTx(nodeID, subnetID, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueAddDelegatorTx(
	vdr *txs.Validator,
	rewardsOwner *secp256k1fx.OutputOwners,
	options ...common.Option,
) (*txs.Tx, error) {
	utx, err := w.builder.NewAddDelegatorTx(vdr, rewardsOwner, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueCreateChainTx(
	subnetID ids.ID,
	genesis []byte,
	vmID ids.ID,
	fxIDs []ids.ID,
	chainName string,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			CreateBlockchainTxFee: w.CreateBlockchainTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewCreateChainTx(subnetID, genesis, vmID, fxIDs, chainName, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueCreateSubnetTx(
	owner *secp256k1fx.OutputOwners,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			CreateSubnetTxFee: w.CreateSubnetTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}
	utx, err := w.dynamicBuilder.NewCreateSubnetTx(owner, feeCalc, options...)
	if err != nil {
		return nil, err
	}

	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueTransferSubnetOwnershipTx(
	subnetID ids.ID,
	owner *secp256k1fx.OutputOwners,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TxFee: w.BaseTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewTransferSubnetOwnershipTx(subnetID, owner, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueImportTx(
	sourceChainID ids.ID,
	to *secp256k1fx.OutputOwners,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TxFee: w.BaseTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewImportTx(sourceChainID, to, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueExportTx(
	chainID ids.ID,
	outputs []*avax.TransferableOutput,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TxFee: w.BaseTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewExportTx(chainID, outputs, feeCalc, options...)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueTransformSubnetTx(
	subnetID ids.ID,
	assetID ids.ID,
	initialSupply uint64,
	maxSupply uint64,
	minConsumptionRate uint64,
	maxConsumptionRate uint64,
	minValidatorStake uint64,
	maxValidatorStake uint64,
	minStakeDuration time.Duration,
	maxStakeDuration time.Duration,
	minDelegationFee uint32,
	minDelegatorStake uint64,
	maxValidatorWeightFactor byte,
	uptimeRequirement uint32,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			TransformSubnetTxFee: w.TransformSubnetTxFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}
	utx, err := w.dynamicBuilder.NewTransformSubnetTx(
		subnetID,
		assetID,
		initialSupply,
		maxSupply,
		minConsumptionRate,
		maxConsumptionRate,
		minValidatorStake,
		maxValidatorStake,
		minStakeDuration,
		maxStakeDuration,
		minDelegationFee,
		minDelegatorStake,
		maxValidatorWeightFactor,
		uptimeRequirement,
		feeCalc,
		options...,
	)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueAddPermissionlessValidatorTx(
	vdr *txs.SubnetValidator,
	signer signer.Signer,
	assetID ids.ID,
	validationRewardsOwner *secp256k1fx.OutputOwners,
	delegationRewardsOwner *secp256k1fx.OutputOwners,
	shares uint32,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			AddPrimaryNetworkValidatorFee: w.AddPrimaryNetworkValidatorFee(),
			AddSubnetValidatorFee:         w.AddSubnetValidatorFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewAddPermissionlessValidatorTx(
		vdr,
		signer,
		assetID,
		validationRewardsOwner,
		delegationRewardsOwner,
		shares,
		feeCalc,
		options...,
	)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueAddPermissionlessDelegatorTx(
	vdr *txs.SubnetValidator,
	assetID ids.ID,
	rewardsOwner *secp256k1fx.OutputOwners,
	options ...common.Option,
) (*txs.Tx, error) {
	if err := w.refreshFork(options...); err != nil {
		return nil, err
	}

	feesMan := commonfees.NewManager(w.unitFees)
	feeCalc := &fees.Calculator{
		IsEUpgradeActive: w.isEForkActive,
		Config: &config.Config{
			AddPrimaryNetworkDelegatorFee: w.AddPrimaryNetworkDelegatorFee(),
			AddSubnetDelegatorFee:         w.AddSubnetDelegatorFee(),
		},
		FeeManager:       feesMan,
		ConsumedUnitsCap: w.unitCaps,
	}

	utx, err := w.dynamicBuilder.NewAddPermissionlessDelegatorTx(
		vdr,
		assetID,
		rewardsOwner,
		feeCalc,
		options...,
	)
	if err != nil {
		return nil, err
	}
	return w.IssueUnsignedTx(utx, options...)
}

func (w *wallet) IssueUnsignedTx(
	utx txs.UnsignedTx,
	options ...common.Option,
) (*txs.Tx, error) {
	ops := common.NewOptions(options)
	ctx := ops.Context()
	tx, err := w.signer.SignUnsigned(ctx, utx)
	if err != nil {
		return nil, err
	}

	return tx, w.IssueTx(tx, options...)
}

func (w *wallet) IssueTx(
	tx *txs.Tx,
	options ...common.Option,
) error {
	ops := common.NewOptions(options)
	ctx := ops.Context()
	txID, err := w.client.IssueTx(ctx, tx.Bytes())
	if err != nil {
		return err
	}

	if f := ops.PostIssuanceFunc(); f != nil {
		f(txID)
	}

	if ops.AssumeDecided() {
		return w.Backend.AcceptTx(ctx, tx)
	}

	txStatus, err := w.client.AwaitTxDecided(ctx, txID, ops.PollFrequency())
	if err != nil {
		return err
	}

	if err := w.Backend.AcceptTx(ctx, tx); err != nil {
		return err
	}

	if txStatus.Status != status.Committed {
		return fmt.Errorf("%w: %s", errNotCommitted, txStatus.Reason)
	}
	return nil
}

func (w *wallet) refreshFork(options ...common.Option) error {
	if w.isEForkActive {
		// E fork enables dinamic fees and it is active
		// not need to recheck
		return nil
	}

	var (
		ops       = common.NewOptions(options)
		ctx       = ops.Context()
		eForkTime = version.GetEUpgradeTime(w.NetworkID())
	)

	chainTime, err := w.client.GetTimestamp(ctx)
	if err != nil {
		return err
	}

	w.isEForkActive = !chainTime.Before(eForkTime)
	if w.isEForkActive {
		w.unitFees = config.EUpgradeDynamicFeesConfig.UnitFees
		w.unitCaps = config.EUpgradeDynamicFeesConfig.BlockUnitsCap
	} else {
		w.unitFees = commonfees.Empty
		w.unitCaps = commonfees.Max
	}

	return nil
}
