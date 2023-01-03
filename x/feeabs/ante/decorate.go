package ante

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	feeabskeeper "github.com/notional-labs/feeabstraction/v1/x/feeabs/keeper"
)

type FeeAbstractionDeductFeeDecorate struct {
	accountKeeper  AccountKeeper
	bankKeeper     bankkeeper.BaseKeeper
	feeabsKeeper   feeabskeeper.Keeper
	feegrantKeeper FeegrantKeeper
}

func NewFeeAbstractionDecorate(ak AccountKeeper, bk bankkeeper.BaseKeeper, feeabsKeeper feeabskeeper.Keeper, fk FeegrantKeeper) FeeAbstractionDeductFeeDecorate {
	return FeeAbstractionDeductFeeDecorate{
		accountKeeper:  ak,
		bankKeeper:     bk,
		feeabsKeeper:   feeabsKeeper,
		feegrantKeeper: fk,
	}
}

func (fadfd FeeAbstractionDeductFeeDecorate) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return ctx, sdkerrors.Wrap(sdkerrors.ErrTxDecode, "Tx must be a FeeTx")
	}
	// normal deduct logic
	if !feeTx.FeePayer().Equals(fadfd.feeabsKeeper.GetModuleAddress()) {
		return fadfd.normalDeductFeeAnteHandle(ctx, tx, simulate, next, feeTx)
	}

	return fadfd.abstractionDeductFeeHandler(ctx, tx, simulate, next, feeTx)
}

func (fadfd FeeAbstractionDeductFeeDecorate) normalDeductFeeAnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler, feeTx sdk.FeeTx) (newCtx sdk.Context, err error) {
	if addr := fadfd.accountKeeper.GetModuleAddress(types.FeeCollectorName); addr == nil {
		return ctx, fmt.Errorf("fee collector module account (%s) has not been set", types.FeeCollectorName)
	}

	fee := feeTx.GetFee()
	feePayer := feeTx.FeePayer()
	feeGranter := feeTx.FeeGranter()

	deductFeesFrom := feePayer

	// if feegranter set deduct fee from feegranter account.
	// this works with only when feegrant enabled.
	if feeGranter != nil {
		if fadfd.feegrantKeeper == nil {
			return ctx, sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "fee grants are not enabled")
		} else if !feeGranter.Equals(feePayer) {
			err := fadfd.feegrantKeeper.UseGrantedFees(ctx, feeGranter, feePayer, fee, tx.GetMsgs())

			if err != nil {
				return ctx, sdkerrors.Wrapf(err, "%s not allowed to pay fees from %s", feeGranter, feePayer)
			}
		}

		deductFeesFrom = feeGranter
	}

	deductFeesFromAcc := fadfd.accountKeeper.GetAccount(ctx, deductFeesFrom)
	if deductFeesFromAcc == nil {
		return ctx, sdkerrors.Wrapf(sdkerrors.ErrUnknownAddress, "fee payer address: %s does not exist", deductFeesFrom)
	}

	// deduct the fees
	if !feeTx.GetFee().IsZero() {
		err = DeductFees(fadfd.bankKeeper, ctx, deductFeesFromAcc, feeTx.GetFee())
		if err != nil {
			return ctx, err
		}
	}

	events := sdk.Events{sdk.NewEvent(sdk.EventTypeTx,
		sdk.NewAttribute(sdk.AttributeKeyFee, feeTx.GetFee().String()),
	)}
	ctx.EventManager().EmitEvents(events)

	return next(ctx, tx, simulate)
}

func (fadfd FeeAbstractionDeductFeeDecorate) abstractionDeductFeeHandler(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler, feeTx sdk.FeeTx) (newCtx sdk.Context, err error) {
	//fee abstraction deduct logic
	deductFeesFrom := fadfd.feeabsKeeper.GetModuleAddress()
	deductFeesFromAcc := fadfd.accountKeeper.GetAccount(ctx, deductFeesFrom)
	if deductFeesFromAcc == nil {
		return ctx, sdkerrors.Wrapf(sdkerrors.ErrUnknownAddress, "fee abstraction didn't set : %s does not exist", deductFeesFrom)
	}

	// calculate the native token can be swapped from ibc token
	ibcFees := feeTx.GetFee()
	if len(ibcFees) != 1 {
		return ctx, sdkerrors.Wrapf(sdkerrors.ErrInvalidCoins, "invalid ibc token: %s", ibcFees)
	}

	nativeFees, err := fadfd.feeabsKeeper.CalculateNativeFromIBCCoin(ibcFees)
	if err != nil {
		return ctx, err
	}

	// deduct the fees
	if !feeTx.GetFee().IsZero() {
		err = DeductFees(fadfd.bankKeeper, ctx, deductFeesFromAcc, nativeFees)
		if err != nil {
			return ctx, err
		}
	}

	events := sdk.Events{sdk.NewEvent(sdk.EventTypeTx,
		sdk.NewAttribute(sdk.AttributeKeyFee, feeTx.GetFee().String()),
	)}
	ctx.EventManager().EmitEvents(events)

	return next(ctx, tx, simulate)

}

// DeductFees deducts fees from the given account.
func DeductFees(bankKeeper types.BankKeeper, ctx sdk.Context, acc types.AccountI, fees sdk.Coins) error {
	if !fees.IsValid() {
		return sdkerrors.Wrapf(sdkerrors.ErrInsufficientFee, "invalid fee amount: %s", fees)
	}

	err := bankKeeper.SendCoinsFromAccountToModule(ctx, acc.GetAddress(), types.FeeCollectorName, fees)
	if err != nil {
		return sdkerrors.Wrapf(sdkerrors.ErrInsufficientFunds, err.Error())
	}

	return nil
}