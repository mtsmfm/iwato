# 0025. Iwato on Windows is a tray app without a console window

- Status: Accepted
- Date: 2026-11-11

## Context

- The Windows exe is a console program: a double-click opens a console window that must stay open while Iwato runs, and closing it stops Iwato. The window carries little the owner needs (the page opens by itself, [ADR 0015](0015-one-click-update-from-github-releases.md)'s updates and the log are in the page)
- Errors that stop Iwato before it serves (the configuration, the port) were printed there with "Press Enter to close"
- Tray libraries: `fyne.io/systray` is pure Go on Windows (cgo on macOS, which the cross builds turn off); writing our own `Shell_NotifyIcon` code is more to keep

## Decision

- The Windows exe is built with `-H windowsgui` and puts an icon in the notification area (`fyne.io/systray`, Windows only): a click opens the page with a new sign-in token, the menu opens the page or the log file, or quits (as Ctrl+C did)
- Errors that stop Iwato are shown in a message box, and the kept port being taken is asked in one (Try Again, Continue with another port this time, Cancel)
- Started from a console, Iwato attaches to it and writes there as before (`version`, `help`, `-verbose`)
- Every child process is started without a console window (`internal/proc`), since a console program started by a GUI program opens one
- macOS and Linux keep the console program

## Consequences

- Nothing to keep open on Windows; the tray is where Iwato is
- The console's lines (the sign-in URL, notices) are seen only from a terminal or in the log; the tray's Open gives a new sign-in URL instead
- A shell does not wait for a GUI program: started from a terminal, the prompt comes back at once while Iwato writes to it; quit it from the tray
- The tray's words follow Windows' display language (Japanese or English)
