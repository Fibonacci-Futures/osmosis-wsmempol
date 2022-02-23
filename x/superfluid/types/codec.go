package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

func RegisterCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgSuperfluidDelegate{}, "osmosis/superfluid/superfluid-delegate", nil)
	cdc.RegisterConcrete(&MsgSuperfluidUndelegate{}, "osmosis/superfluid/superfluid-undelegate", nil)
	cdc.RegisterConcrete(&MsgLockAndSuperfluidDelegate{}, "osmosis/superfluid/lock-and-superfluid-delegate", nil)
	cdc.RegisterConcrete(&MsgSuperfluidUnbondLock{}, "osmosis/superfluid/superfluid-unbond-lock", nil)

	cdc.RegisterConcrete(&SetSuperfluidAssetsProposal{}, "osmosis/superfluid/set-superfluid-assets-proposal", nil)
	cdc.RegisterConcrete(&RemoveSuperfluidAssetsProposal{}, "osmosis/superfluid/remove-superfluid-assets-proposal", nil)
}

func RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgSuperfluidDelegate{},
		&MsgSuperfluidUndelegate{},
		// &MsgSuperfluidRedelegate{},
		&MsgLockAndSuperfluidDelegate{},
		&MsgSuperfluidUnbondLock{},
	)

	registry.RegisterImplementations(
		(*govtypes.Content)(nil),
		&SetSuperfluidAssetsProposal{},
		&RemoveSuperfluidAssetsProposal{},
	)

	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}

var (
	amino     = codec.NewLegacyAmino()
	ModuleCdc = codec.NewAminoCodec(amino)
)

func init() {
	RegisterCodec(amino)
	amino.Seal()
}
