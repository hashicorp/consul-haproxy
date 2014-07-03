package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/mitchellh/mapstructure"
)

// WatchRE is used to parse a backend configuration. The config should
// look like "backend=tag.service@datacenter:port". However, the tag, port and
// datacenter are optional, so it can also be provided as "backend=service"
var WatchRE = regexp.MustCompile("^([^=]+)=([^.]+\\.)?([^.:@]+)(@[^.:]+)?(:[0-9]+)?$")

// WatchPath represents a path we need to watch
type WatchPath struct {
	Spec       string
	Backend    string
	Service    string
	Tag        string
	Datacenter string
	Port       int
}

// Config is used to configure the HAProxy connector
type Config struct {
	// DryRun is used to avoid actually modifying the file
	// or reloading HAProxy.
	DryRun bool

	// Address is the Consul HTTP API address
	Address string `mapstructure:"address"`

	// Path to the HAProxy template file
	Template string `mapstructure:"tempalte"`

	// Path to the HAProxy configuration file to write
	Path string `mapstructure:"path"`

	// Command used to reload HAProxy
	ReloadCommand string `mapstructure:"reload_command"`

	// Backends are used to specify what we watch. Given as:
	// "name=(tag.)service"
	Backends []string `mapstructure:"backends"`

	// watches are the watches we need to track
	watches []*WatchPath
}

func main() {
	os.Exit(realMain())
}

// realMain is the actual entry point, but we wrap it to set
// a proper exit code on return
func realMain() int {
	var configFile string
	var backends []string
	conf := &Config{}
	flag.Usage = usage
	flag.StringVar(&conf.Address, "addr", "127.0.0.1:8500", "consul HTTP API address with port")
	flag.StringVar(&conf.Template, "template", "", "template path")
	flag.StringVar(&conf.Path, "path", "", "config path")
	flag.StringVar(&conf.ReloadCommand, "reload", "", "reload command")
	flag.StringVar(&configFile, "f", "", "config file")
	flag.BoolVar(&conf.DryRun, "dry", false, "dry run")
	flag.Var((*AppendSliceValue)(&backends), "backend", "backend to populate")
	flag.Parse()

	// Parse the configuration file if given
	if configFile != "" {
		if err := readConfig(configFile, conf); err != nil {
			log.Printf("[ERR] Failed to read config file: %v", err)
			return 1
		}
	}

	// Merge the backends together
	conf.Backends = append(conf.Backends, backends...)

	// Sanity check the configuration
	if errs := validateConfig(conf); len(errs) != 0 {
		for _, err := range errs {
			log.Printf("[ERR] %v", err)
		}
		return 1
	}

	// Start watching for changes
	stopCh, finishCh := watch(conf)

	// Wait for termination
	return waitForTerm(conf, stopCh, finishCh)
}

// readConfig is used to read a configuration file
func readConfig(path string, config *Config) error {
	// Read the file
	contents, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	// Decode the file
	var raw interface{}
	if err := json.NewDecoder(bytes.NewReader(contents)).Decode(&raw); err != nil {
		return err
	}

	// Map to our output
	if err := mapstructure.Decode(raw, config); err != nil {
		return err
	}
	return nil
}

// validateConfig is used to sanity check the configuration
func validateConfig(conf *Config) (errs []error) {
	// Check the template
	if conf.Template == "" {
		errs = append(errs, errors.New("missing template path"))
	} else {
		_, err := ioutil.ReadFile(conf.Template)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read template: %v", err))
		}
	}

	if conf.Path == "" {
		errs = append(errs, errors.New("missing configuration path"))
	}

	if conf.ReloadCommand == "" {
		errs = append(errs, errors.New("missing reload command"))
	}

	if len(conf.Backends) == 0 {
		errs = append(errs, errors.New("missing backends to populate"))
	}

	for _, b := range conf.Backends {
		parts := WatchRE.FindStringSubmatch(b)
		if parts == nil || len(parts) != 6 {
			errs = append(errs, fmt.Errorf("Backend '%s' could not be parsed", b))
			continue
		}
		var port int
		if parts[5] != "" {
			p, err := strconv.ParseInt(strings.TrimPrefix(parts[5], ":"), 10, 64)
			if err != nil {
				errs = append(errs, fmt.Errorf("Backend '%s' port could not be parsed", b))
				continue
			}
			port = int(p)
		}
		wp := &WatchPath{
			Spec:       parts[0],
			Backend:    parts[1],
			Tag:        strings.TrimSuffix(parts[2], "."),
			Service:    parts[3],
			Datacenter: strings.TrimPrefix(parts[4], "@"),
			Port:       port,
		}
		conf.watches = append(conf.watches, wp)
	}

	return
}

// waitForTerm waits until we receive a signal to exit
func waitForTerm(conf *Config, stopCh, finishCh chan struct{}) int {
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGHUP)
	for {
		select {
		case sig := <-signalCh:
			switch sig {
			case syscall.SIGHUP:
				// TODO: Handle reload
				log.Printf("[WARN] SIGHUP received, reloading configuration...")

				// Stop the existing watcher
				close(stopCh)

				// Start a new watcher
				stopCh, finishCh = watch(conf)

			default:
				log.Printf("[WARN] Received %v signal, shutting down", sig)
				return 0
			}
		case <-finishCh:
			if conf.DryRun {
				return 0
			}
			log.Printf("[WARN] Aborting watching for changes, shutting down")
			return 1
		}
	}
	return 0
}

func usage() {
	cmd := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, strings.TrimSpace(helpText)+"\n\n", cmd)
}

const helpText = `
Usage: %s [options]

  Watches a service group in Consul and dynamically configures
  an HAProxy backend.

Options:

  -addr=127.0.0.1:8500  Provides the HTTP address of a Consul agent.
`
