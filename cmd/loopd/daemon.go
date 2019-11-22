package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync"
	"time"

	proxy "github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/lightninglabs/loop"
	"github.com/lightninglabs/loop/cmd/loopd/server"
	"github.com/lightninglabs/loop/looprpc"
	"google.golang.org/grpc"
)

// daemon runs loopd in daemon mode. It will listen for grpc connections,
// execute commands and pass back swap status information.
func daemon(config *config) error {
	lnd, err := getLnd(config.Network, config.Lnd)
	if err != nil {
		return err
	}
	defer lnd.Close()

	// If no swap server is specified, use the default addresses for mainnet
	// and testnet.
	if config.SwapServer == "" {
		switch config.Network {
		case "mainnet":
			config.SwapServer = mainnetServer
		case "testnet":
			config.SwapServer = testnetServer
		default:
			return errors.New("no swap server address specified")
		}
	}

	log.Infof("Swap server address: %v", config.SwapServer)

	// Create an instance of the loop client library.
	swapClient, cleanup, err := getClient(
		config.Network, config.SwapServer, config.Insecure,
		&lnd.LndServices,
	)
	if err != nil {
		return err
	}
	defer cleanup()

	statusChan := make(chan loop.SwapInfo)
	mainCtx, cancel := context.WithCancel(context.Background())

	// Instantiate the loopd gRPC server.
	server, err := server.New(
		mainCtx, swapClient, &lnd.LndServices, statusChan,
	)
	if err != nil {
		return err
	}

	serverOpts := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(serverOpts...)
	looprpc.RegisterSwapClientServer(grpcServer, server)

	// Next, start the gRPC server listening for HTTP/2 connections.
	log.Infof("Starting gRPC listener")
	grpcListener, err := net.Listen("tcp", config.RPCListen)
	if err != nil {
		return fmt.Errorf("RPC server unable to listen on %s",
			config.RPCListen)

	}
	defer grpcListener.Close()

	// We'll also create and start an accompanying proxy to serve clients
	// through REST.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := proxy.NewServeMux()
	proxyOpts := []grpc.DialOption{grpc.WithInsecure()}
	err = looprpc.RegisterSwapClientHandlerFromEndpoint(
		ctx, mux, config.RPCListen, proxyOpts,
	)
	if err != nil {
		return err
	}

	log.Infof("Starting REST proxy listener")
	restListener, err := net.Listen("tcp", config.RESTListen)
	if err != nil {
		return fmt.Errorf("REST proxy unable to listen on %s",
			config.RESTListen)
	}
	defer restListener.Close()
	proxy := &http.Server{Handler: mux}
	go proxy.Serve(restListener)

	var wg sync.WaitGroup

	// Start the swap client itself.
	wg.Add(1)
	go func() {
		defer wg.Done()

		log.Infof("Starting swap client")
		err := swapClient.Run(mainCtx, statusChan)
		if err != nil {
			log.Error(err)
		}
		log.Infof("Swap client stopped")

		log.Infof("Stopping gRPC server")
		grpcServer.Stop()

		cancel()
	}()

	// Start the grpc server.
	wg.Add(1)
	go func() {
		defer wg.Done()

		log.Infof("RPC server listening on %s", grpcListener.Addr())
		log.Infof("REST proxy listening on %s", restListener.Addr())

		err = grpcServer.Serve(grpcListener)
		if err != nil {
			log.Error(err)
		}
	}()

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt)

	// Run until the users terminates loopd or an error occurred.
	select {
	case <-interruptChannel:
		log.Infof("Received SIGINT (Ctrl+C).")

		// TODO: Remove debug code.
		// Debug code to dump goroutines on hanging exit.
		go func() {
			time.Sleep(5 * time.Second)
			pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
		}()

		cancel()
	case <-mainCtx.Done():
	}

	wg.Wait()

	return nil
}
