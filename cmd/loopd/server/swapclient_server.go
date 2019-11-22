package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/queue"

	"github.com/lightninglabs/loop"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/loopdb"
	"github.com/lightninglabs/loop/swap"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/loop/looprpc"
)

const (
	completedSwapsCount = 5

	// minConfTarget is the minimum confirmation target we'll allow clients
	// to specify. This is driven by the minimum confirmation target allowed
	// by the backing fee estimator.
	minConfTarget = 2

	defaultConfTarget = int32(6)
)

var (
	swaps            = make(map[lntypes.Hash]loop.SwapInfo)
	subscribers      = make(map[int]chan<- interface{})
	nextSubscriberID int
	swapsLock        sync.Mutex
)

// swapClientServer implements the grpc service exposed by loopd.
type swapClientServer struct {
	impl *loop.Client
	lnd  *lndclient.LndServices
}

func New(mainCtx context.Context, swapClient *loop.Client,
	lnd *lndclient.LndServices, statusChan <-chan loop.SwapInfo,
) (looprpc.SwapClientServer, error) {

	// Retrieve all currently existing swaps from the database.
	swapsList, err := swapClient.FetchSwaps()
	if err != nil {
		return nil, err
	}

	for _, s := range swapsList {
		swaps[s.SwapHash] = *s
	}

	// Start a goroutine that broadcasts swap updates to clients.
	//	wg.Add(1)
	go func() {
		//		defer wg.Done()

		log.Infof("Waiting for updates")
		for {
			select {
			case swap := <-statusChan:
				swapsLock.Lock()
				swaps[swap.SwapHash] = swap

				for _, subscriber := range subscribers {
					select {
					case subscriber <- swap:
					case <-mainCtx.Done():
						return
					}
				}

				swapsLock.Unlock()
			case <-mainCtx.Done():
				return
			}
		}
	}()

	return &swapClientServer{
		impl: swapClient,
		lnd:  lnd,
	}, nil
}

// LoopOut initiates an loop out swap with the given parameters. The call
// returns after the swap has been set up with the swap server. From that point
// onwards, progress can be tracked via the LoopOutStatus stream that is
// returned from Monitor().
func (s *swapClientServer) LoopOut(ctx context.Context,
	in *looprpc.LoopOutRequest) (
	*looprpc.SwapResponse, error) {

	log.Infof("Loop out request received")

	sweepConfTarget, err := validateConfTarget(
		in.SweepConfTarget, loop.DefaultSweepConfTarget,
	)
	if err != nil {
		return nil, err
	}

	var sweepAddr btcutil.Address
	if in.Dest == "" {
		// Generate sweep address if none specified.
		var err error
		sweepAddr, err = s.lnd.WalletKit.NextAddr(context.Background())
		if err != nil {
			return nil, fmt.Errorf("NextAddr error: %v", err)
		}
	} else {
		var err error
		sweepAddr, err = btcutil.DecodeAddress(
			in.Dest, s.lnd.ChainParams,
		)
		if err != nil {
			return nil, fmt.Errorf("decode address: %v", err)
		}
	}

	req := &loop.OutRequest{
		Amount:              btcutil.Amount(in.Amt),
		DestAddr:            sweepAddr,
		MaxMinerFee:         btcutil.Amount(in.MaxMinerFee),
		MaxPrepayAmount:     btcutil.Amount(in.MaxPrepayAmt),
		MaxPrepayRoutingFee: btcutil.Amount(in.MaxPrepayRoutingFee),
		MaxSwapRoutingFee:   btcutil.Amount(in.MaxSwapRoutingFee),
		MaxSwapFee:          btcutil.Amount(in.MaxSwapFee),
		SweepConfTarget:     sweepConfTarget,
	}
	if in.LoopOutChannel != 0 {
		req.LoopOutChannel = &in.LoopOutChannel
	}
	hash, htlc, err := s.impl.LoopOut(ctx, req)
	if err != nil {
		log.Errorf("LoopOut: %v", err)
		return nil, err
	}

	return &looprpc.SwapResponse{
		Id:          hash.String(),
		HtlcAddress: htlc.String(),
	}, nil
}

func (s *swapClientServer) marshallSwap(loopSwap *loop.SwapInfo) (
	*looprpc.SwapStatus, error) {

	var state looprpc.SwapState
	switch loopSwap.State {
	case loopdb.StateInitiated:
		state = looprpc.SwapState_INITIATED
	case loopdb.StatePreimageRevealed:
		state = looprpc.SwapState_PREIMAGE_REVEALED
	case loopdb.StateHtlcPublished:
		state = looprpc.SwapState_HTLC_PUBLISHED
	case loopdb.StateInvoiceSettled:
		state = looprpc.SwapState_INVOICE_SETTLED
	case loopdb.StateSuccess:
		state = looprpc.SwapState_SUCCESS
	default:
		// Return less granular status over rpc.
		state = looprpc.SwapState_FAILED
	}

	var swapType looprpc.SwapType
	switch loopSwap.SwapType {
	case swap.TypeIn:
		swapType = looprpc.SwapType_LOOP_IN
	case swap.TypeOut:
		swapType = looprpc.SwapType_LOOP_OUT
	default:
		return nil, errors.New("unknown swap type")
	}

	return &looprpc.SwapStatus{
		Amt:            int64(loopSwap.AmountRequested),
		Id:             loopSwap.SwapHash.String(),
		State:          state,
		InitiationTime: loopSwap.InitiationTime.UnixNano(),
		LastUpdateTime: loopSwap.LastUpdate.UnixNano(),
		HtlcAddress:    loopSwap.HtlcAddress.EncodeAddress(),
		Type:           swapType,
		CostServer:     int64(loopSwap.Cost.Server),
		CostOnchain:    int64(loopSwap.Cost.Onchain),
		CostOffchain:   int64(loopSwap.Cost.Offchain),
	}, nil
}

// Monitor will return a stream of swap updates for currently active swaps.
func (s *swapClientServer) Monitor(in *looprpc.MonitorRequest,
	server looprpc.SwapClient_MonitorServer) error {

	log.Infof("Monitor request received")

	send := func(info loop.SwapInfo) error {
		rpcSwap, err := s.marshallSwap(&info)
		if err != nil {
			return err
		}

		return server.Send(rpcSwap)
	}

	// Start a notification queue for this subscriber.
	queue := queue.NewConcurrentQueue(20)
	queue.Start()

	// Add this subscriber to the global subscriber list. Also create a
	// snapshot of all pending and completed swaps within the lock, to
	// prevent subscribers from receiving duplicate updates.
	swapsLock.Lock()

	id := nextSubscriberID
	nextSubscriberID++
	subscribers[id] = queue.ChanIn()

	var pendingSwaps, completedSwaps []loop.SwapInfo
	for _, swap := range swaps {
		if swap.State.Type() == loopdb.StateTypePending {
			pendingSwaps = append(pendingSwaps, swap)
		} else {
			completedSwaps = append(completedSwaps, swap)
		}
	}

	swapsLock.Unlock()

	defer func() {
		queue.Stop()
		swapsLock.Lock()
		delete(subscribers, id)
		swapsLock.Unlock()
	}()

	// Sort completed swaps new to old.
	sort.Slice(completedSwaps, func(i, j int) bool {
		return completedSwaps[i].LastUpdate.After(
			completedSwaps[j].LastUpdate,
		)
	})

	// Discard all but top x latest.
	if len(completedSwaps) > completedSwapsCount {
		completedSwaps = completedSwaps[:completedSwapsCount]
	}

	// Concatenate both sets.
	filteredSwaps := append(pendingSwaps, completedSwaps...)

	// Sort again, but this time old to new.
	sort.Slice(filteredSwaps, func(i, j int) bool {
		return filteredSwaps[i].LastUpdate.Before(
			filteredSwaps[j].LastUpdate,
		)
	})

	// Return swaps to caller.
	for _, swap := range filteredSwaps {
		if err := send(swap); err != nil {
			return err
		}
	}

	// As long as the client is connected, keep passing through swap
	// updates.
	for {
		select {
		case queueItem, ok := <-queue.ChanOut():
			if !ok {
				return nil
			}

			swap := queueItem.(loop.SwapInfo)
			if err := send(swap); err != nil {
				return err
			}
		case <-server.Context().Done():
			return nil
		}
	}
}

// LoopOutTerms returns the terms that the server enforces for loop out swaps.
func (s *swapClientServer) LoopOutTerms(ctx context.Context,
	req *looprpc.TermsRequest) (*looprpc.TermsResponse, error) {

	log.Infof("Loop out terms request received")

	terms, err := s.impl.LoopOutTerms(ctx)
	if err != nil {
		log.Errorf("Terms request: %v", err)
		return nil, err
	}

	return &looprpc.TermsResponse{
		MinSwapAmount: int64(terms.MinSwapAmount),
		MaxSwapAmount: int64(terms.MaxSwapAmount),
	}, nil
}

// LoopOutQuote returns a quote for a loop out swap with the provided
// parameters.
func (s *swapClientServer) LoopOutQuote(ctx context.Context,
	req *looprpc.QuoteRequest) (*looprpc.QuoteResponse, error) {

	confTarget, err := validateConfTarget(
		req.ConfTarget, loop.DefaultSweepConfTarget,
	)
	if err != nil {
		return nil, err
	}
	quote, err := s.impl.LoopOutQuote(ctx, &loop.LoopOutQuoteRequest{
		Amount:          btcutil.Amount(req.Amt),
		SweepConfTarget: confTarget,
	})
	if err != nil {
		return nil, err
	}

	return &looprpc.QuoteResponse{
		MinerFee:        int64(quote.MinerFee),
		PrepayAmt:       int64(quote.PrepayAmount),
		SwapFee:         int64(quote.SwapFee),
		SwapPaymentDest: quote.SwapPaymentDest[:],
		CltvDelta:       quote.CltvDelta,
	}, nil
}

// GetTerms returns the terms that the server enforces for swaps.
func (s *swapClientServer) GetLoopInTerms(ctx context.Context, req *looprpc.TermsRequest) (
	*looprpc.TermsResponse, error) {

	log.Infof("Loop in terms request received")

	terms, err := s.impl.LoopInTerms(ctx)
	if err != nil {
		log.Errorf("Terms request: %v", err)
		return nil, err
	}

	return &looprpc.TermsResponse{
		MinSwapAmount: int64(terms.MinSwapAmount),
		MaxSwapAmount: int64(terms.MaxSwapAmount),
	}, nil
}

// GetQuote returns a quote for a swap with the provided parameters.
func (s *swapClientServer) GetLoopInQuote(ctx context.Context,
	req *looprpc.QuoteRequest) (*looprpc.QuoteResponse, error) {

	log.Infof("Loop in quote request received")

	quote, err := s.impl.LoopInQuote(ctx, &loop.LoopInQuoteRequest{
		Amount:         btcutil.Amount(req.Amt),
		HtlcConfTarget: defaultConfTarget,
		ExternalHtlc:   req.ExternalHtlc,
	})
	if err != nil {
		return nil, err
	}
	return &looprpc.QuoteResponse{
		MinerFee: int64(quote.MinerFee),
		SwapFee:  int64(quote.SwapFee),
	}, nil
}

func (s *swapClientServer) LoopIn(ctx context.Context,
	in *looprpc.LoopInRequest) (
	*looprpc.SwapResponse, error) {

	log.Infof("Loop in request received")

	req := &loop.LoopInRequest{
		Amount:         btcutil.Amount(in.Amt),
		MaxMinerFee:    btcutil.Amount(in.MaxMinerFee),
		MaxSwapFee:     btcutil.Amount(in.MaxSwapFee),
		HtlcConfTarget: defaultConfTarget,
		ExternalHtlc:   in.ExternalHtlc,
	}
	if in.LoopInChannel != 0 {
		req.LoopInChannel = &in.LoopInChannel
	}
	hash, htlc, err := s.impl.LoopIn(ctx, req)
	if err != nil {
		log.Errorf("Loop in: %v", err)
		return nil, err
	}

	return &looprpc.SwapResponse{
		Id:          hash.String(),
		HtlcAddress: htlc.String(),
	}, nil
}

// validateConfTarget ensures the given confirmation target is valid. If one
// isn't specified (0 value), then the default target is used.
func validateConfTarget(target, defaultTarget int32) (int32, error) {
	switch {
	// Ensure the target respects our minimum threshold.
	case target < minConfTarget:
		return 0, fmt.Errorf("a confirmation target of at least %v "+
			"must be provided", minConfTarget)

	default:
		return target, nil
	}
}
