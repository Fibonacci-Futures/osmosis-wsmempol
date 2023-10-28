package redis

import (
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/osmosis-labs/osmosis/osmomath"
	"github.com/osmosis-labs/osmosis/v20/ingest"
	"github.com/osmosis-labs/osmosis/v20/ingest/sqs/domain"
	"github.com/osmosis-labs/osmosis/v20/ingest/sqs/pools/common"
	"github.com/osmosis-labs/osmosis/v20/x/concentrated-liquidity/client/queryproto"
	concentratedtypes "github.com/osmosis-labs/osmosis/v20/x/concentrated-liquidity/types"
	poolmanagertypes "github.com/osmosis-labs/osmosis/v20/x/poolmanager/types"
)

// poolIngester is an ingester for pools.
// It implements ingest.Ingester.
// It reads all pools from the state and writes them to the pools repository.
// As part of that, it instruments each pool with chain native balances and
// OSMO based TVL.
// NOTE:
// - TVL is calculated using spot price. TODO: use TWAP (https://app.clickup.com/t/86a182835)
// - TVL does not account for token precision. TODO: use assetlist for pulling token precision data
// (https://app.clickup.com/t/86a18287v)
// - If error in TVL calculation, TVL is set to the value that could be computed and the pool struct
// has a flag to indicate that there was an error in TVL calculation.
type poolIngester struct {
	poolsRepository    domain.PoolsRepository
	repositoryManager  domain.TxManager
	gammKeeper         common.PoolKeeper
	concentratedKeeper common.ConcentratedKeeper
	cosmWasmKeeper     common.CosmWasmPoolKeeper
	bankKeeper         common.BankKeeper
	protorevKeeper     common.ProtorevKeeper
	poolManagerKeeper  common.PoolManagerKeeper
}

// denomRoutingInfo encapsulates the routing information for a pool.
// It has a pool ID of the pool that is paired with OSMO.
// It has a spot price from that pool with OSMO as the base asset.
type denomRoutingInfo struct {
	PoolID uint64
	Price  osmomath.BigDec
}

const UOSMO = "uosmo"

// NewPoolIngester returns a new pool ingester.
func NewPoolIngester(poolsRepository domain.PoolsRepository, repositoryManager domain.TxManager, gammKeeper common.PoolKeeper, concentratedKeeper common.ConcentratedKeeper, cosmwasmKeeper common.CosmWasmPoolKeeper, bankKeeper common.BankKeeper, protorevKeeper common.ProtorevKeeper, poolManagerKeeper common.PoolManagerKeeper) ingest.AtomicIngester {
	return &poolIngester{
		poolsRepository:    poolsRepository,
		repositoryManager:  repositoryManager,
		gammKeeper:         gammKeeper,
		concentratedKeeper: concentratedKeeper,
		cosmWasmKeeper:     cosmwasmKeeper,
		bankKeeper:         bankKeeper,
		protorevKeeper:     protorevKeeper,
		poolManagerKeeper:  poolManagerKeeper,
	}
}

// ProcessBlock implements ingest.Ingester.
func (pi *poolIngester) ProcessBlock(ctx sdk.Context, tx domain.Tx) error {
	return pi.updatePoolState(ctx, tx)
}

var _ ingest.AtomicIngester = &poolIngester{}

func (pi *poolIngester) updatePoolState(ctx sdk.Context, tx domain.Tx) error {
	goCtx := sdk.WrapSDKContext(ctx)

	// Create a map from denom to routable pool ID.
	denomToRoutablePoolIDMap := make(map[string]denomRoutingInfo)

	// CFMM pools

	cfmmPools, err := pi.gammKeeper.GetPools(ctx)
	if err != nil {
		return err
	}

	// Parse CFMM pool to the standard SQS types.
	cfmmPoolsParsed := make([]domain.PoolI, 0, len(cfmmPools))
	for _, pool := range cfmmPools {
		// Parse CFMM pool to the standard SQS types.
		pool, err := convertPool(ctx, pool, denomToRoutablePoolIDMap, pi.bankKeeper, pi.protorevKeeper, pi.poolManagerKeeper, pi.concentratedKeeper)
		if err != nil {
			return err
		}

		cfmmPoolsParsed = append(cfmmPoolsParsed, pool)
	}

	// Concentrated pools

	concentratedPools, err := pi.concentratedKeeper.GetPools(ctx)
	if err != nil {
		return err
	}

	concentratedPoolsParsed := make([]domain.PoolI, 0, len(concentratedPools))
	for _, pool := range concentratedPools {
		// Parse concentrated pool to the standard SQS types.
		pool, err := convertPool(ctx, pool, denomToRoutablePoolIDMap, pi.bankKeeper, pi.protorevKeeper, pi.poolManagerKeeper, pi.concentratedKeeper)
		if err != nil {
			return err
		}

		concentratedPoolsParsed = append(concentratedPoolsParsed, pool)
	}

	// CosmWasm pools

	cosmWasmPools, err := pi.cosmWasmKeeper.GetPoolsWithWasmKeeper(ctx)
	if err != nil {
		return err
	}

	cosmWasmPoolsParsed := make([]domain.PoolI, 0, len(cosmWasmPools))
	for _, pool := range cosmWasmPools {
		// Parse cosmwasm pool to the standard SQS types.
		pool, err := convertPool(ctx, pool, denomToRoutablePoolIDMap, pi.bankKeeper, pi.protorevKeeper, pi.poolManagerKeeper, pi.concentratedKeeper)
		if err != nil {
			return err
		}

		cosmWasmPoolsParsed = append(cosmWasmPoolsParsed, pool)
	}

	err = pi.poolsRepository.StorePools(goCtx, tx, cfmmPoolsParsed, concentratedPoolsParsed, cosmWasmPoolsParsed)
	if err != nil {
		return err
	}

	return nil
}

// convertPool converts a pool to the standard SQS pool type.
// It instruments the pool with chain native balances and OSMO based TVL.
// If error occurs in TVL estimation, it is silently skipped and the error flag
// set to true in the pool model.
// Note:
// - TVL is calculated using spot price. TODO: use TWAP (https://app.clickup.com/t/86a182835)
// - TVL does not account for token precision. TODO: use assetlist for pulling token precision data
// (https://app.clickup.com/t/86a18287v)
func convertPool(
	ctx sdk.Context,
	pool poolmanagertypes.PoolI,
	denomToRoutingInfoMap map[string]denomRoutingInfo,
	bankKeeper common.BankKeeper,
	protorevKeeper common.ProtorevKeeper,
	poolManagerKeeper common.PoolManagerKeeper,
	concentratedKeeper common.ConcentratedKeeper,
) (domain.PoolI, error) {
	balances := bankKeeper.GetAllBalances(ctx, pool.GetAddress())

	osmoPoolTVL := osmomath.ZeroInt()

	isErrorInTVL := false
	for _, balance := range balances {
		if balance.Denom == UOSMO {
			osmoPoolTVL = osmoPoolTVL.Add(balance.Amount)
			continue
		}

		// Check if routable poolID already exists for the denom
		routingInfo, ok := denomToRoutingInfoMap[balance.Denom]
		if !ok {
			poolForDenomPair, err := protorevKeeper.GetPoolForDenomPair(ctx, UOSMO, balance.Denom)
			if err != nil {
				ctx.Logger().Error("error getting OSMO-based pool", "denom", balance.Denom, "error", err)
				isErrorInTVL = true
				continue
			}

			uosmoBaseAssetSpotPrice, err := poolManagerKeeper.RouteCalculateSpotPrice(ctx, poolForDenomPair, balance.Denom, UOSMO)
			if err != nil {
				ctx.Logger().Error("error calculating spot price for denom", "denom", balance.Denom, "error", err)
				isErrorInTVL = true
				continue
			}

			routingInfo = denomRoutingInfo{
				PoolID: poolForDenomPair,
				Price:  uosmoBaseAssetSpotPrice,
			}
		}

		osmoPoolTVL = osmoPoolTVL.Add(osmomath.NewBigDecFromBigInt(balance.Amount.BigInt()).MulMut(routingInfo.Price).Dec().TruncateInt())
	}

	// Get pool denoms. Although these can be inferred from balances, this is safer.
	// If we used balances, for pools with no liquidity, we would not be able to get the denoms.
	denoms, err := poolManagerKeeper.RouteGetPoolDenoms(ctx, pool.GetId())
	if err != nil {
		return nil, err
	}

	// Get the tick model for concentrated pools
	var tickModel *domain.TickModel

	// For CL pools, get the tick data
	if pool.GetType() == poolmanagertypes.Concentrated {
		tickData, currentTickIndex, err := concentratedKeeper.GetTickLiquidityForFullRange(ctx, pool.GetId())
		// If there is no error, we set the tick model
		if err == nil {
			tickModel = &domain.TickModel{
				Ticks:            tickData,
				CurrentTickIndex: currentTickIndex,
			}
			// If there is no liquidity, we set the tick model to nil and update no liquidity flag
		} else if err != nil && errors.Is(err, concentratedtypes.RanOutOfTicksForPoolError{PoolId: pool.GetId()}) {
			tickModel = &domain.TickModel{
				Ticks:            []queryproto.LiquidityDepthWithRange{},
				CurrentTickIndex: -1,
				HasNoLiquidity:   true,
			}

			// On any other error, we return the error
		} else {
			return nil, err
		}
	}

	return &domain.PoolWrapper{
		ChainModel: pool,
		SQSModel: domain.SQSPool{
			TotalValueLockedUSDC:      osmoPoolTVL,
			IsErrorInTotalValueLocked: isErrorInTVL,
			Balances:                  balances,
			PoolDenoms:                denoms,
		},
		TickModel: tickModel,
	}, nil
}