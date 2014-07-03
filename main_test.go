package main

import (
	"reflect"
	"testing"
)

func TestWatchRE(t *testing.T) {
	type match struct {
		inp   string
		parts []string
	}
	inps := []match{
		{"bar", nil},
		{"bar=", nil},
		{"=zip", nil},
		{"app=bar", []string{"app", "", "bar", "", ""}},
		{"app=tag.bar", []string{"app", "tag.", "bar", "", ""}},
		{"app=bar@dc1", []string{"app", "", "bar", "@dc1", ""}},
		{"app=bar:80", []string{"app", "", "bar", "", ":80"}},
		{"app=bar@dc1:80", []string{"app", "", "bar", "@dc1", ":80"}},
		{"app=tag.bar@dc1:80", []string{"app", "tag.", "bar", "@dc1", ":80"}},
	}

	for _, inp := range inps {
		parts := WatchRE.FindStringSubmatch(inp.inp)
		if len(parts) == 0 && len(inp.parts) != 0 {
			t.Fatalf("unexpected fail: %s", inp.inp)
		}
		if len(parts) != 0 && len(inp.parts) == 0 {
			t.Fatalf("unexpected parse: %s", inp.inp)
		}
		if len(parts) == 0 && len(inp.parts) == 0 {
			continue
		}
		if !reflect.DeepEqual(parts[1:], inp.parts) {
			t.Fatalf("bad: %v %v", parts[1:], inp.parts)
		}
	}
}

func TestReadConfig(t *testing.T) {
	conf := &Config{}
	err := readConfig("test-fixtures/config.json", conf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !conf.DryRun {
		t.Fatalf("bad: %v", conf)
	}
	if conf.Address != "127.0.0.2:8500" {
		t.Fatalf("bad: %v", conf)
	}
	if conf.Template != "test-fixtures/simple.conf" {
		t.Fatalf("bad: %v", conf)
	}
	if conf.Path != "output.conf" {
		t.Fatalf("bad: %v", conf)
	}
	if conf.ReloadCommand != "echo 'foo' > reload_out" {
		t.Fatalf("bad: %v", conf)
	}
	backends := []string{
		"app=foo",
		"app=tag.foo",
		"app=tag.foo@dc2",
	}
	if !reflect.DeepEqual(conf.Backends, backends) {
		t.Fatalf("bad: %v", conf)
	}
}
