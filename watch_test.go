package main

import (
	"bytes"
	"github.com/armon/consul-api"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

func TestAggregateServers(t *testing.T) {
	en1 := &consulapi.ServiceEntry{
		Node:    &consulapi.Node{Node: "node1", Address: "127.0.0.1"},
		Service: &consulapi.AgentService{ID: "app", Port: 8000},
	}
	en2 := &consulapi.ServiceEntry{
		Node:    &consulapi.Node{Node: "node3", Address: "127.0.0.3"},
		Service: &consulapi.AgentService{ID: "app", Port: 8000},
	}
	en3 := &consulapi.ServiceEntry{
		Node:    &consulapi.Node{Node: "node2", Address: "127.0.0.2"},
		Service: &consulapi.AgentService{ID: "db", Port: 5000},
	}
	wp1 := &WatchPath{Backend: "app"}
	wp2 := &WatchPath{Backend: "app"}
	wp3 := &WatchPath{Backend: "db"}
	d := &backendData{
		Servers: map[*WatchPath][]*consulapi.ServiceEntry{
			wp1: []*consulapi.ServiceEntry{en1},
			wp2: []*consulapi.ServiceEntry{en2},
			wp3: []*consulapi.ServiceEntry{en3},
		},
		Backends: map[string][]*WatchPath{
			"app": []*WatchPath{wp1, wp2},
			"db":  []*WatchPath{wp3},
		},
	}
	agg := aggregateServers(d)
	if len(agg) != 2 {
		t.Fatalf("Bad: %v", agg)
	}
	app := agg["app"]
	if len(app) != 2 {
		t.Fatalf("Bad: %v", app)
	}
	if app[0] != en1 && app[1] != en2 {
		t.Fatalf("Bad: %v", app)
	}
	db := agg["db"]
	if len(db) != 1 {
		t.Fatalf("Bad: %v", db)
	}
	if db[0] != en3 {
		t.Fatalf("Bad: %v", db)
	}
}

func TestBuildTemplate(t *testing.T) {
	conf := &Config{
		Template: "test-fixtures/simple.conf",
	}
	servers := map[string][]*consulapi.ServiceEntry{
		"app": []*consulapi.ServiceEntry{
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node1", Address: "127.0.0.1"},
				Service: &consulapi.AgentService{ID: "app", Port: 8000},
			},
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node3", Address: "127.0.0.3"},
				Service: &consulapi.AgentService{ID: "app", Port: 8000},
			},
		},
	}

	out, err := buildTemplate(conf, servers)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	expect, err := ioutil.ReadFile("test-fixtures/simple.conf.out")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if !bytes.Equal(out, expect) {
		t.Fatalf("bad: %s", out)
	}
}

func TestReload(t *testing.T) {
	os.Remove("test_out")
	conf := &Config{
		ReloadCommand: "echo 'foo' > test_out",
	}
	if err := reload(conf); err != nil {
		t.Fatalf("err: %v", err)
	}
	bytes, err := ioutil.ReadFile("test_out")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(bytes) != "foo\n" {
		t.Fatalf("bad: %v", bytes)
	}
	os.Remove("test_out")
}

func TestShouldStop(t *testing.T) {
	ch := make(chan struct{})
	if shouldStop(ch) {
		t.Fatalf("bad")
	}
	close(ch)
	if !shouldStop(ch) {
		t.Fatalf("bad")
	}
}

func TestAsyncNotify(t *testing.T) {
	ch := make(chan struct{}, 1)
	asyncNotify(ch)
	asyncNotify(ch)
	asyncNotify(ch)

	select {
	case <-ch:
	default:
		t.Fatalf("should work")
	}
	select {
	case <-ch:
		t.Fatalf("should not work")
	default:
	}

}

func TestMin(t *testing.T) {
	if min(1, 2) != 1 {
		t.Fatalf("Bad")
	}
	if min(2, 1) != 1 {
		t.Fatalf("Bad")
	}
}

func TestBackoff(t *testing.T) {
	type val struct {
		fail   int
		expect time.Duration
	}
	inps := []val{
		{1, 3 * time.Second},
		{2, 6 * time.Second},
		{3, 12 * time.Second},
		{4, 24 * time.Second},
	}
	for _, inp := range inps {
		if out := backoff(3*time.Second, inp.fail); out != inp.expect {
			t.Fatalf("bad: %v %v", inp, out)
		}
	}
}

func TestFormatOutput(t *testing.T) {
	inp := map[string][]*consulapi.ServiceEntry{
		"foo": []*consulapi.ServiceEntry{
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node1", Address: "127.0.0.1"},
				Service: &consulapi.AgentService{ID: "redis", Port: 8000},
			},
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node3", Address: "127.0.0.3"},
				Service: &consulapi.AgentService{ID: "redis", Port: 1234},
			},
		},
		"bar": []*consulapi.ServiceEntry{
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node2", Address: "127.0.0.2"},
				Service: &consulapi.AgentService{ID: "memcache", Port: 80},
			},
			&consulapi.ServiceEntry{
				Node:    &consulapi.Node{Node: "node4", Address: "127.0.0.4"},
				Service: &consulapi.AgentService{ID: "memcache", Port: 10000},
			},
		},
	}

	output := formatOutput(inp)
	if len(output) != 2 {
		t.Fatalf("bad: %v", output)
	}
	foo := output["foo"]
	if len(foo) != 2 {
		t.Fatalf("bad: %v", foo)
	}
	if foo[0] != "server node1_redis 127.0.0.1:8000" {
		t.Fatalf("Bad: %v", foo)
	}
	if foo[1] != "server node3_redis 127.0.0.3:1234" {
		t.Fatalf("Bad: %v", foo)
	}

	bar := output["bar"]
	if len(bar) != 2 {
		t.Fatalf("bad: %v", bar)
	}
	if bar[0] != "server node2_memcache 127.0.0.2:80" {
		t.Fatalf("Bad: %v", bar)
	}
	if bar[1] != "server node4_memcache 127.0.0.4:10000" {
		t.Fatalf("Bad: %v", bar)
	}
}
