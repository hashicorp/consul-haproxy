package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"text/template"
	"time"

	"github.com/armon/consul-api"
)

const (
	// failSleep controls how long to sleep on a failure
	failSleep = 5 * time.Second

	// maxFailures controls the maximum number of failures
	// before we limit the sleep value
	maxFailures = 5

	// waitTime is used to control how long we do a blocking
	// query for
	waitTime = 60 * time.Second
)

type backendData struct {
	sync.Mutex

	// Client is a shared Consul client
	Client *consulapi.Client

	// Servers maps each watch path to a list of entries
	Servers map[*WatchPath][]*consulapi.ServiceEntry

	// Backends maps a backend to a list of watch paths used
	// to build up the server list
	Backends map[string][]*WatchPath

	// ChangeCh is used to inform of an update
	ChangeCh chan struct{}

	// StopCh is used to trigger a stop
	StopCh chan struct{}
}

// watch is used to start a long running watcher to handle updates.
// Returns a stopCh, and a finishCh.
func watch(conf *Config) (chan struct{}, chan struct{}) {
	stopCh := make(chan struct{})
	finishCh := make(chan struct{})
	go runWatch(conf, stopCh, finishCh)
	return stopCh, finishCh
}

// runWatch is a long running routine that watches with a
// given configuration
func runWatch(conf *Config, stopCh, doneCh chan struct{}) {
	defer close(doneCh)

	// Create the consul client
	consulConf := consulapi.DefaultConfig()
	if conf.Address != "" {
		consulConf.Address = conf.Address
	}

	// Attempt to contact the agent
	client, err := consulapi.NewClient(consulConf)
	if err != nil {
		log.Printf("[ERR] Failed to initialize consul client: %v", err)
		return
	}
	if _, err := client.Agent().NodeName(); err != nil {
		log.Printf("[ERR] Failed to contact consul agent: %v", err)
		return
	}

	// Create a backend store
	data := &backendData{
		Client:   client,
		Servers:  make(map[*WatchPath][]*consulapi.ServiceEntry),
		Backends: make(map[string][]*WatchPath),
		ChangeCh: make(chan struct{}, 1),
		StopCh:   stopCh,
	}

	// Start the watches
	data.Lock()
	for idx, watch := range conf.watches {
		data.Backends[watch.Backend] = append(data.Backends[watch.Backend], watch)
		go runSingleWatch(conf, data, idx, watch)
	}
	data.Unlock()

	// Monitor for changes or stop
	for {
		select {
		case <-data.ChangeCh:
			if maybeRefresh(conf, data) {
				return
			}

		case <-stopCh:
			return
		}
	}
}

// maybeRefresh is used to handle a potential config update
func maybeRefresh(conf *Config, data *backendData) (exit bool) {
	// Ignore initial updates until all the data is ready
	if !allWatchesReturned(conf, data) {
		return
	}

	// Merge the data for each backend
	backendServers := aggregateServers(data)

	// Build the output template
	output, err := buildTemplate(conf, backendServers)
	if err != nil {
		log.Printf("[ERR] %v", err)
		return true
	}

	// Check for a dry run
	if conf.DryRun {
		fmt.Printf("%s\n", output)
		return true
	}

	// Write out the configuration
	if err := ioutil.WriteFile(conf.Path, output, 0660); err != nil {
		log.Printf("[ERR] Failed to write config file: %v", err)
		return true
	}
	log.Printf("[INFO] Updated configuration file at %s", conf.Path)

	// Invoke the reload hook
	if err := reload(conf); err != nil {
		log.Printf("[ERR] Failed to reload: %v", err)
	} else {
		log.Printf("[INFO] Completed reload")
	}
	return
}

// allWatchesReturned checks if all the watches have some
// data registered. Prevents early template generation.
func allWatchesReturned(conf *Config, data *backendData) bool {
	data.Lock()
	defer data.Unlock()
	return len(data.Servers) >= len(conf.watches)
}

// aggregateServers merges the watches belonging to each
// backend together to prepare for template generation
func aggregateServers(data *backendData) map[string][]*consulapi.ServiceEntry {
	backendServers := make(map[string][]*consulapi.ServiceEntry)
	data.Lock()
	defer data.Unlock()
	for backend, watches := range data.Backends {
		var all []*consulapi.ServiceEntry
		for _, watch := range watches {
			entries := data.Servers[watch]
			all = append(all, entries...)
		}
		backendServers[backend] = all
	}
	return backendServers
}

// buildTemplate is used to build the output template
// from the configuration and server list
func buildTemplate(conf *Config,
	servers map[string][]*consulapi.ServiceEntry) ([]byte, error) {
	// Format the output
	outVars := formatOutput(servers)

	// Read the template
	raw, err := ioutil.ReadFile(conf.Template)
	if err != nil {
		return nil, fmt.Errorf("Failed to read template: %v", err)
	}

	// Create the template
	templ, err := template.New("output").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("Failed to parse the template: %v", err)
	}

	// Generate the output
	var output bytes.Buffer
	if err := templ.Execute(&output, outVars); err != nil {
		return nil, fmt.Errorf("Failed to generate the template: %v", err)
	}
	return output.Bytes(), nil
}

// runSingleWatch is used to query a single watch path for changes
func runSingleWatch(conf *Config, data *backendData, idx int, watch *WatchPath) {
	health := data.Client.Health()
	opts := &consulapi.QueryOptions{
		WaitTime: waitTime,
	}
	if watch.Datacenter != "" {
		opts.Datacenter = watch.Datacenter
	}

	failures := 0
	for {
		if shouldStop(data.StopCh) {
			return
		}
		entries, qm, err := health.Service(watch.Service, watch.Tag, true, opts)
		if err != nil {
			log.Printf("[ERR] Failed to fetch service nodes: %v", err)
		}

		// Patch the entries as necessary
		for _, entry := range entries {
			// Modify the node name to prefix with the watch ID. This
			// prevents a name conflict on duplicate names
			entry.Node.Node = fmt.Sprintf("%d_%s", idx, entry.Node.Node)

			// Patch the port if provided
			if watch.Port != 0 {
				entry.Service.Port = watch.Port
			}
		}

		// Update the entries. If this is the first read, do it on error
		data.Lock()
		old, ok := data.Servers[watch]
		if !ok || (err == nil && !reflect.DeepEqual(old, entries)) {
			data.Servers[watch] = entries
			asyncNotify(data.ChangeCh)
			if !conf.DryRun {
				log.Printf("[DEBUG] Updated nodes for %v", watch.Spec)
			}
		}
		data.Unlock()

		// Stop immediately on a dry run
		if conf.DryRun {
			return
		}

		// Check for an error
		if err != nil {
			failures = min(failures+1, maxFailures)
			time.Sleep(backoff(failSleep, failures))
		} else {
			failures = 0
			opts.WaitIndex = qm.LastIndex
		}
	}
}

// reload is used to invoke the reload command
func reload(conf *Config) error {
	// Determine the shell invocation based on OS
	var shell, flag string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		flag = "/C"
	} else {
		shell = "/bin/sh"
		flag = "-c"
	}

	// Create and invoke the command
	cmd := exec.Command(shell, flag, conf.ReloadCommand)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// shouldStop checks for a closed control channel
func shouldStop(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// asyncNotify is used to notify a channel
func asyncNotify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// min returns the min of two ints
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// backoff is used to compute an exponential backoff
func backoff(interval time.Duration, times int) time.Duration {
	times--
	return interval * time.Duration(math.Pow(2, float64(times)))
}

// formatOutput converts the service entries into a format
// suitable for templating into the HAProxy file
func formatOutput(inp map[string][]*consulapi.ServiceEntry) map[string][]string {
	out := make(map[string][]string)
	for backend, entries := range inp {
		servers := make([]string, len(entries))
		for idx, entry := range entries {
			name := fmt.Sprintf("%s_%s", entry.Node.Node, entry.Service.ID)
			ip := net.ParseIP(entry.Node.Address)
			addr := &net.TCPAddr{IP: ip, Port: entry.Service.Port}
			servers[idx] = fmt.Sprintf("server %s %s", name, addr)
		}
		out[backend] = servers
	}
	return out
}
