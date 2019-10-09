package loopmobile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/go-flags"
	"github.com/lightninglabs/loop"
	"github.com/lightninglabs/loop/cmd/loopd/server"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/looprpc"
	"github.com/lightningnetwork/lnd"
	"google.golang.org/grpc"
)

const (
	mainnetServer = "swap.lightning.today:11009"
	testnetServer = "test.swap.lightning.today:11009"
)

var (
	loopDirBase = btcutil.AppDataDir("loop", false)
)

// Start starts lnd and loop in new goroutines.
//
// extraArgs can be used to pass command line arguments to lnd that will
// override what is found in the config file. Example:
//	extraArgs = "--bitcoin.testnet --lnddir=\"/tmp/folder name/\" --profile=5050"
func Start(extraArgs string, callback Callback) {
	// Split the argument string on "--" to get separated command line
	// arguments.
	var splitArgs []string
	for _, a := range strings.Split(extraArgs, "--") {
		if a == "" {
			continue
		}
		// Finally we prefix any non-empty string with --, and trim
		// whitespace to mimic the regular command line arguments.
		splitArgs = append(splitArgs, strings.TrimSpace("--"+a))
	}

	// Add the extra arguments to os.Args, as that will be parsed during
	// startup.
	os.Args = append(os.Args, splitArgs...)

	// We call the main method with the custom in-memory listeners called
	// by the mobile APIs, such that the grpc server will use these.
	cfg := lnd.ListenerCfg{
		WalletUnlocker: walletUnlockerLis,
		RPCListener:    lightningLis,
	}

	// Call the "real" main in a nested manner so the defers will properly
	// be executed in the case of a graceful shutdown.
	go func() {
		if err := lnd.Main(cfg); err != nil {
			if e, ok := err.(*flags.Error); ok &&
				e.Type == flags.ErrHelp {
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(1)
		}
	}()

	// Get a connection to the lnd instance we just started.
	lndConn, _, err := getLightningConn()
	if err != nil {
		callback.OnError(err)
		return
	}

	// TODO: configurable.
	// --no-macaroons?
	macDir := ""
	network := "testnet"

	// Use the obtained connection to back a new instance of LndServices.
	lnd, err := lndclient.NewLndServicesFromConn(
		lndConn, network, macDir,
	)
	if err != nil {
		callback.OnError(err)
		return
	}

	// Create an instance of the loop client library.
	storeDir, err := getStoreDir(network)
	if err != nil {
		callback.OnError(err)
		return

	}

	// Create the swap client that will be talking to the swap server.
	// TODO: must have TLS
	swapClient, cleanUp, err := loop.NewClient(
		storeDir, testnetServer, true, &lnd.LndServices,
	)
	if err != nil {
		callback.OnError(err)
		return

	}

	mainCtx, cancel := context.WithCancel(context.Background())
	statusChan := make(chan loop.SwapInfo)

	// Instantiate the loopd gRPC server.
	server, err := server.New(
		mainCtx, swapClient, &lnd.LndServices, statusChan,
	)
	if err != nil {
		callback.OnError(err)
		return
	}

	serverOpts := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(serverOpts...)
	looprpc.RegisterSwapClientServer(grpcServer, server)

	var wg sync.WaitGroup

	// Start the swap client itself.
	wg.Add(1)
	go func() {
		defer wg.Done()

		err := swapClient.Run(mainCtx, statusChan)
		if err != nil {
			callback.OnError(err)
			return
		}
		grpcServer.Stop()

		cancel()
		cleanUp()
	}()

	// Start the grpc server.
	wg.Add(1)
	go func() {
		defer wg.Done()

		err = grpcServer.Serve(swapClientLis)
		if err != nil {
			callback.OnError(err)
			return
		}
	}()

	callback.OnResponse([]byte("started"))
	return
}

func getStoreDir(network string) (string, error) {
	dir := filepath.Join(loopDirBase, network)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return "", err
	}

	return dir, nil
}
