//go:build linux

// Command iwato-entrypoint is Iwato's daemon in a sandbox: it keeps the
// sandbox's terminals (internal/termd) and serves its home over SFTP
// (internal/rootsftp). The sandbox image carries it at
// /usr/local/lib/iwato/iwato-entrypoint (termd.Path; the WSLc driver
// places it at termd.LegacyPath for sandboxes that run it there), and
// Iwato runs:
//
//	iwato-entrypoint serve -key KEY [-detached]
//	iwato-entrypoint attach [-spawn] -key KEY [-cols N -rows N] NAME [-- CMD ARG...]
//	iwato-entrypoint list [-spawn] -key KEY
//	iwato-entrypoint kill -key KEY NAME
//	iwato-entrypoint sftp -root DIR
//
// serve is the daemon (a new sandbox's main process; -detached: one that
// attach -spawn started, which leaves when another serves the key). attach,
// list and kill talk to it and write termd messages to stdout, errors
// included: Iwato runs them through an exec that merges stderr into
// stdout, so they never write to stderr. sftp serves DIR (the home) over
// SFTP on stdin and stdout, as the exec's user, and writes nothing else.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"iwato/internal/rootsftp"
	"iwato/internal/termd"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	key := fs.String("key", "default", "the daemon's key (Iwato's sandbox name)")
	spawn := fs.Bool("spawn", false, "start the daemon when none runs")
	detached := fs.Bool("detached", false, "serve: leave when another daemon serves the key")
	cols := fs.Int("cols", 0, "attach: columns (0: keep)")
	rows := fs.Int("rows", 0, "attach: rows (0: keep)")
	root := fs.String("root", "", "sftp: the folder served (the home)")
	if err := fs.Parse(args); err != nil {
		fail(termd.ErrInvalid, err.Error())
	}
	if !termd.ValidKey(*key) {
		fail(termd.ErrInvalid, "invalid key")
	}
	switch cmd {
	case "sftp":
		// Nothing but SFTP may reach stdout: a failure only exits.
		if *root == "" {
			os.Exit(2)
		}
		if err := rootsftp.Serve(*root, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
	case "serve":
		err := termd.Serve(*key, !*detached)
		if errors.Is(err, termd.ErrRunning) {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "iwato-entrypoint:", err)
			os.Exit(1)
		}
	case "attach":
		if fs.NArg() < 1 || !termd.ValidName(fs.Arg(0)) {
			fail(termd.ErrInvalid, "attach needs a terminal name")
		}
		o := termd.Open{Name: fs.Arg(0), Cols: *cols, Rows: *rows}
		rest := fs.Args()[1:]
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			// The new terminal's environment is this exec's: what OpenShell
			// gives it now (the providers attached), not the daemon's.
			o.Cmd, o.Env = rest, os.Environ()
			o.Dir, _ = os.Getwd()
		}
		c := dial(*key, *spawn)
		if err := termd.Relay(c, o, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
	case "list":
		if err := termd.Request(dial(*key, *spawn), termd.KindList, nil, os.Stdout); err != nil {
			fail(termd.ErrUnavailable, err.Error())
		}
	case "kill":
		if fs.NArg() != 1 || !termd.ValidName(fs.Arg(0)) {
			fail(termd.ErrInvalid, "kill needs a terminal name")
		}
		if err := termd.Request(dial(*key, false), termd.KindKill, termd.Kill{Name: fs.Arg(0)}, os.Stdout); err != nil {
			fail(termd.ErrUnavailable, err.Error())
		}
	default:
		usage()
	}
}

func dial(key string, spawn bool) termd.Conn {
	var c termd.Conn
	var err error
	if spawn {
		self, e := os.Executable()
		if e != nil {
			fail(termd.ErrUnavailable, e.Error())
		}
		c, err = termd.DialOrSpawn(key, self)
	} else {
		c, err = termd.Dial(key)
	}
	if err != nil {
		fail(termd.ErrUnavailable, "the terminal daemon is not running: "+err.Error())
	}
	return c
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: iwato-entrypoint serve|attach|list|kill [-key KEY] ... | sftp -root DIR")
	os.Exit(2)
}

// fail writes an error message (stdout: see the package comment) and exits.
func fail(code, msg string) {
	_ = termd.WriteJSON(os.Stdout, termd.KindError, termd.Error{Code: code, Message: msg})
	os.Exit(1)
}
