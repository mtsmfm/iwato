// Command e2eprobe checks, for e2e.sh, what Iwato needs from the gateway
// beyond the CLI: stdin streamed to a command without a terminal.
//
//	e2eprobe ADDRESS CA CERT KEY SANDBOX
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"iwato/internal/openshell/client"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("STREAM-OK")
}

func run(a []string) error {
	conn, err := client.Dial(a[0], a[1], a[2], a[3])
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := client.Start(ctx, conn, "default", a[4], []string{"cat"}, client.Options{})
	if err != nil {
		return err
	}
	defer p.Close()
	r := bufio.NewReader(p)
	// Each line must come back before the next is sent: stdin streams.
	for _, line := range []string{"ping-1", "ping-2"} {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			return err
		}
		got, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read %s: %w", line, err)
		}
		if strings.TrimSpace(got) != line {
			return fmt.Errorf("got %q, want %q", got, line)
		}
	}
	if err := p.CloseWrite(); err != nil {
		return err
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		return fmt.Errorf("exit %d: %v", code, err)
	}
	return nil
}
