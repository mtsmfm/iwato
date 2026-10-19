// Command openshell-driver-wslc runs Iwato's OpenShell compute driver for
// WSLc on its own (iwato serve runs it in process). The gateway dials it
// over a Unix socket; on Windows the gateway runs in a WSLc container and a
// relay there forwards the socket to this process over TLS
// (host.wslc.internal reaches Windows' loopback).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"iwato/internal/openshell/wslc"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "certs" {
		// The driver's listener certificates and the relay's client one.
		if err := wslc.WriteCerts(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	listen := flag.String("listen", "unix:///run/openshell/wslc.sock", "unix:///PATH, tcp://HOST:PORT or tls://HOST:PORT")
	tlsCert := flag.String("tls-cert", "", "tls:// listener certificate")
	tlsKey := flag.String("tls-key", "", "tls:// listener key")
	tlsClientCA := flag.String("tls-client-ca", "", "tls:// listener: CA of the accepted client certificates")
	pullPolicy := flag.String("image-pull-policy", "if-not-present", "if-not-present, always or never")
	guestCA := flag.String("guest-tls-ca", "", "gateway CA staged for supervisors (https endpoint)")
	wslcExe := flag.String("wslc", "wslc.exe", "wslc executable")
	endpoint := flag.String("gateway-endpoint", "http://openshell-gateway:17670", "gateway address the supervisors call (OPENSHELL_ENDPOINT)")
	network := flag.String("network", "openshell", "WSLc network shared by the gateway and the supervisors")
	supervisorImage := flag.String("supervisor-image", wslc.DefaultSupervisorImage, "supervisor image")
	sandboxImage := flag.String("sandbox-image", wslc.DefaultSandboxImage, "image carrying openshell-sandbox")
	defaultImage := flag.String("default-image", "nvcr.io/nvidia/base/ubuntu:24.04", "workload image when the caller names none")
	namespace := flag.String("namespace", "default", "label value that marks this driver's sandboxes")
	logLevel := flag.String("log-level", "info", "log level passed to sandboxes and supervisors")
	stateDir := flag.String("state-dir", wslc.DefaultStateDir(), "where each sandbox's boundary files are kept")
	shellInit := flag.Bool("shell-init", false, "experiment: run the sandbox under /bin/sh so that it is not PID 1")
	legacyTerm := flag.String("legacy-term", "", "Iwato's daemon, placed at its legacy path in the sandboxes whose main process runs it from there (none when empty)")
	flag.Parse()
	var term []byte
	if *legacyTerm != "" {
		b, err := os.ReadFile(*legacyTerm)
		if err != nil {
			log.Fatal(err)
		}
		term = b
	}

	ln, err := wslc.Listen(*listen, *tlsCert, *tlsKey, *tlsClientCA)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = wslc.Serve(ctx, wslc.Config{
		Endpoint:        *endpoint,
		Network:         *network,
		SupervisorImage: *supervisorImage,
		SandboxImage:    *sandboxImage,
		DefaultImage:    *defaultImage,
		Namespace:       *namespace,
		StateDir:        *stateDir,
		LogLevel:        *logLevel,
		PullPolicy:      *pullPolicy,
		GuestTLSCA:      *guestCA,
		ShellInit:       *shellInit,
		LegacyTerm:      term,
	}, *wslcExe, ln)
	if err != nil {
		log.Fatal(err)
	}
}
