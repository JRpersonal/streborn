package boxcli

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A Pandora or iHeartRadio key goes back exactly as the speaker reported it.
// A value the TAP CLI would split is refused instead of written mangled.
func TestAddPresetContentItemRefusesValuesTheCLICannotCarry(t *testing.T) {
	ctx := context.Background()
	for name, args := range map[string][5]string{
		"space in location": {"PANDORA", "stationurl", "a b", "x", "listener@example.com"},
		"quote in account":  {"PANDORA", "stationurl", "1", "x", `a"b`},
		"no source":         {"", "stationurl", "1", "x", ""},
		"no location":       {"PANDORA", "stationurl", "", "x", ""},
	} {
		if err := AddPresetContentItem(ctx, "192.0.2.1", 1, args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := AddPresetContentItem(ctx, "192.0.2.1", 7, "PANDORA", "stationurl", "1", "x", ""); err == nil {
		t.Error("slot 7 accepted")
	}
}

func TestAddPresetContentItemSendsTheItemVerbatim(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:17000")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:17000 (in use?): %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 4)
	var reply atomic.Value
	reply.Store("OK")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			got <- strings.TrimSpace(line)
			_, _ = c.Write([]byte(reply.Load().(string) + "\n"))
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := AddPresetContentItem(ctx, "127.0.0.1", 2, "PANDORA", "stationurl", "4071226281950183516",
		"Little Big Town Radio", "listener@example.com"); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	want := `ws AddPreset PANDORA stationurl 4071226281950183516 "Little Big Town Radio" listener@example.com 2`
	if cmd := <-got; cmd != want {
		t.Fatalf("command\n got  %s\n want %s", cmd, want)
	}
	// An empty account goes out as "none", which the firmware stores as "".
	if err := AddPresetContentItem(ctx, "127.0.0.1", 3, "IHEART", "", "live:1", "Z100", ""); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if cmd := <-got; cmd != `ws AddPreset IHEART stationurl live:1 "Z100" none 3` {
		t.Fatalf("command %s", cmd)
	}
	reply.Store("AddPreset - failed due to invalid SourceID")
	if err := AddPresetContentItem(ctx, "127.0.0.1", 4, "PANDORA", "stationurl", "1", "x", ""); err == nil {
		t.Fatal("a firmware refusal read as success")
	}
	<-got
}
