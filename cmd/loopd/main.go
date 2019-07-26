package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	flags "github.com/jessevdk/go-flags"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/loop"
	"github.com/lightningnetwork/lnd/lntypes"
)

const (
	defaultConfTarget        = int32(6)
	defaultCutoffTimeSeconds = 30
)

var (
	loopDirBase           = btcutil.AppDataDir("loop", false)
	defaultConfigFilename = "loopd.conf"

	swaps            = make(map[lntypes.Hash]loop.SwapInfo)
	subscribers      = make(map[int]chan<- interface{})
	nextSubscriberID int
	swapsLock        sync.Mutex
)

func main() {
	err := start()
	if err != nil {
		fmt.Println(err)
	}
}

func start() error {
	config := defaultConfig

	// Parse command line flags.
	parser := flags.NewParser(&config, flags.Default)
	parser.SubcommandsOptional = true

	_, err := parser.Parse()
	if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
		return nil
	}
	if err != nil {
		return err
	}

	// Parse ini file.
	loopDir := filepath.Join(loopDirBase, config.Network)
	if err := os.MkdirAll(loopDir, os.ModePerm); err != nil {
		return err
	}

	configFile := filepath.Join(loopDir, defaultConfigFilename)
	if err := flags.IniParse(configFile, &config); err != nil {
		// If it's a parsing related error, then we'll return
		// immediately, otherwise we can proceed as possibly the config
		// file doesn't exist which is OK.
		if _, ok := err.(*flags.IniError); ok {
			return err
		}
	}

	// Parse command line flags again to restore flags overwritten by ini
	// parse.
	_, err = parser.Parse()
	if err != nil {
		return err
	}

	// Show the version and exit if the version flag was specified.
	appName := filepath.Base(os.Args[0])
	appName = strings.TrimSuffix(appName, filepath.Ext(appName))
	if config.ShowVersion {
		fmt.Println(appName, "version", loop.Version())
		os.Exit(0)
	}

	// Print the version before executing either primary directive.
	logger.Infof("Version: %v", loop.Version())

	// Execute command.
	if parser.Active == nil {
		return daemon(&config)
	}

	if parser.Active.Name == "view" {
		return view(&config)
	}

	return fmt.Errorf("unimplemented command %v", parser.Active.Name)
}
