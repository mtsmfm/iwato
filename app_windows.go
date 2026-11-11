package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// On Windows Iwato is a GUI program (built with -H windowsgui: no console
// window) that lives in the notification area: a click opens the page, the
// menu opens the page or the log, or quits (docs/adr/0025). Started from a
// console it writes there; errors that stop it are shown in a message box.

//go:embed tray.ico
var trayIcon []byte

var (
	procAttachConsole    = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procMessageBox       = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
)

// hasConsole: Iwato writes to a console (it was started from one, or built
// without -H windowsgui).
var hasConsole bool

func app(args []string) int {
	hasConsole = attachConsole()
	if len(args) > 0 && args[0] != "serve" {
		return report(run(args))
	}
	ctx, quit := context.WithCancel(context.Background())
	defer quit()
	appCtx = ctx
	done := make(chan error, 1)
	systray.Run(func() {
		w := trayText()
		systray.SetIcon(trayIcon)
		systray.SetTooltip("Iwato")
		open := systray.AddMenuItem(w.open, "")
		logs := systray.AddMenuItem(w.log, "")
		systray.AddSeparator()
		exit := systray.AddMenuItem(w.quit, "")
		// Called on the tray's event loop: nothing that blocks.
		systray.SetOnTapped(func() { go openApp() })
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openApp()
				case <-logs.ClickedCh:
					if a := currentApp.Load(); a != nil && a.logFile != "" {
						_ = openURL(a.logFile)
					}
				case <-exit.ClickedCh:
					quit()
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			done <- run(args)
			systray.Quit()
		}()
	}, nil)
	// The tray can also end first (the session ending).
	quit()
	select {
	case err := <-done:
		return report(err)
	case <-time.After(10 * time.Second):
		return 1
	}
}

func openApp() {
	if a := currentApp.Load(); a != nil && a.open != nil {
		a.open()
	}
}

// report shows an error that stopped Iwato, on the console or in a message
// box, and gives the exit code.
func report(err error) int {
	if err == nil {
		return 0
	}
	if hasConsole {
		fmt.Fprintln(os.Stderr, "Error:", err)
	} else {
		messageBox(trayText().failed+"\n\n"+err.Error(), mbIconError)
	}
	return 1
}

// startPrompt asks in a message box whether to retry the kept port, use
// another one this time, or quit.
func startPrompt() portPrompt {
	return func(port int, owner string) portChoice {
		w := trayText()
		if owner == "" {
			owner = w.anotherProgram
		}
		switch messageBox(fmt.Sprintf(w.portInUse, port, owner, port), mbCancelTryContinue|mbIconWarning) {
		case idTryAgain:
			return portRetry
		case idContinue:
			return portAnother
		}
		return portQuit
	}
}

// attachConsole writes to the console Iwato was started from, if any. A
// redirected output (an update running `iwato version`) is kept as it is.
func attachConsole() bool {
	if w, _, _ := procGetConsoleWindow.Call(); w != 0 {
		return true
	}
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return false
	}
	const attachParent = ^uintptr(0)
	if r, _, _ := procAttachConsole.Call(attachParent); r == 0 {
		return false
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
		log.SetOutput(f)
		notice.SetOutput(f)
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
	return true
}

const (
	mbIconError         = 0x10
	mbIconWarning       = 0x30
	mbCancelTryContinue = 0x6
	mbSetForeground     = 0x10000
	idTryAgain          = 10
	idContinue          = 11
)

func messageBox(text string, flags uintptr) int {
	t, _ := windows.UTF16PtrFromString(text)
	title, _ := windows.UTF16PtrFromString("Iwato")
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(title)), flags|mbSetForeground)
	return int(r)
}

// trayWords are the tray's and the message boxes' words, in Windows'
// display language (Japanese or English, as the page).
type trayWords struct {
	open, log, quit, failed, anotherProgram, portInUse string
}

func trayText() trayWords {
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil && len(langs) > 0 && strings.HasPrefix(langs[0], "ja") {
		return trayWords{
			open: "Iwato を開く", log: "ログを開く", quit: "終了",
			failed:         "Iwato を起動できませんでした。",
			anotherProgram: "別のプログラム",
			portInUse:      "Iwato の画面用に覚えているポート %d を、%s が使っています。\n\n再試行: もう一度確かめます\n続行: 今回だけ別のポートで起動します (インストールしたアプリとログインはポート %d のものです)\nキャンセル: 終了します",
		}
	}
	return trayWords{
		open: "Open Iwato", log: "Open the log", quit: "Quit",
		failed:         "Iwato could not start.",
		anotherProgram: "another program",
		portInUse:      "Port %d, which Iwato keeps for its page, is in use by %s.\n\nTry Again: check again\nContinue: use another port this time (the installed app and the sign-ins belong to %d)\nCancel: quit",
	}
}
