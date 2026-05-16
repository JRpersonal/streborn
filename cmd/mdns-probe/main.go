// mdns-probe directly browses _streborn._tcp.local using the same
// zeroconf library the desktop app uses, prints every entry it sees.
// Helps isolate whether a "no boxes found" report is a discovery-stack
// regression or something higher up.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/JRpersonal/streborn/discovery"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	fmt.Println("Browsing _streborn._tcp.local for 8 seconds...")
	results, err := discovery.Browse(ctx, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browse error:", err)
		os.Exit(1)
	}
	count := 0
	for inst := range results {
		count++
		fmt.Printf("\nFOUND #%d\n", count)
		fmt.Printf("  Name:         %s\n", inst.Name)
		fmt.Printf("  Host:         %s\n", inst.Host)
		fmt.Printf("  IPv4:         %v\n", inst.IPv4)
		fmt.Printf("  Port:         %d\n", inst.Port)
		fmt.Printf("  DeviceID:     %s\n", inst.DeviceID)
		fmt.Printf("  FriendlyName: %s\n", inst.FriendlyName)
		fmt.Printf("  Model:        %s\n", inst.Model)
		fmt.Printf("  Version:      %s\n", inst.Version)
	}
	if count == 0 {
		fmt.Println("\nNo sticks announced _streborn._tcp.local in 8s.")
		os.Exit(2)
	}
	fmt.Printf("\nTotal: %d stick(s)\n", count)
}
