# Terminals

[日本語](ja/terminals.md)

## Terminals keep running

Iwato itself keeps each terminal's screen and scrollback, so you can close the browser or reopen it on a phone and continue where you were. The terminals' processes belong to `iwato-entrypoint`, Iwato's small daemon in the standard image (the sandbox's first process; it also serves the files' SFTP). So:

- Restarting or updating Iwato does not end the main terminal or the extra ones. When you open them again, the screen comes back from the last output (up to 1 MiB)
- Ending the main shell with `exit` ends only that terminal; the next time you open it, a new shell starts
- Open more terminals with ＋ (**New terminal**) above the terminal
- In a sandbox whose image has no daemon, extra terminals end when Iwato exits
- When the image's daemon is older than Iwato, what it supports keeps working and the rest falls back (the files and the editor show the working directory); recreating the sandbox brings the new daemon

On a phone, the on-screen keyboard does not change the terminal's rows (so lines are not duplicated in the history). The key row adds Esc, Tab, ⇧Tab, Ctrl, the arrow keys, ^C, ^B, **Paste**, **Image**, `|`, `/`, `~` and `-`.

## Links and text

- Links (in the ⋯ menu) lists the URLs printed in the terminal's history, newest first. They open only when you tap them, in a new tab
- Text (in the ⋯ menu) shows the history as plain text, to select with the OS (long-press on a phone) or **Copy all**
- Programs in the sandbox can open a URL in a new tab of your browser: the standard image sets `BROWSER=iwato-browser` in terminals. It works only for http(s) URLs, only in the sandbox you are looking at, and only within a few seconds of your own input
- Programs can also write your clipboard (OSC 52), only right after your own input

## Pasting images

Paste an image into a terminal (a screenshot, any paste without text) or drop one onto it. Iwato saves it in the sandbox's home under `.cache/iwato/paste/` and pastes its path. Claude Code reads a pasted image path as the image, as when you drop a file onto a local terminal. On a phone, choose it with the key row's Image.

- PNG, JPEG, GIF and WebP, up to 20 MB
- Only the newest 50 images (within 7 days) are kept
- When the clipboard also has text (for example spreadsheet cells), only the text is pasted

## Notifications

Turn on push notifications with **Turn on notifications** in the sidebar on each device. Iwato sends one:

- for an agent's network proposal (proposals within one minute are grouped)
- when a terminal has finished working: its screen kept changing for 20 seconds or more, then stood still for a minute (for Claude Code and Codex, when their "working" line goes away). Changes within a few seconds of your own typing, scrolling, opening or closing, clocks and spinners, and one-off redraws do not count as work, but a screen that keeps ticking every few seconds (a percentage, a spinner, a timer) is still working. Nothing is sent when the screen is back to what you last saw
- when a program rings the bell (BEL) or sends a desktop notification (OSC 9, OSC 777, OSC 99): at once, with its text (at most once a minute per terminal)

Tip: in Claude Code, set `/config` → Notifications to "Terminal bell" to be notified as soon as it finishes.

Like Slack, notifications are sent only when no device is using Iwato (a page that is shown and was used within the last 5 minutes). While you use it, the page title shows the number of pending approvals, and stopped terminals get a mark in the sidebar and on the terminal's tab.

Push notifications need a secure context: `http://127.0.0.1` on the PC, or the HTTPS address through Tailscale on a phone ([phone access](publishing.md#use-iwato-from-your-phone)).

## Terminal recordings

To investigate a broken screen later, add `"termTrace": true` to `iwato.json`. Each terminal then records its output from when it opens, inside the sandbox in `~/.local/state/iwato/term-trace/<sandbox>-<terminal>-<time>.jsonl` (mode 0600; the home persists). Nothing is stored on the host.

- One JSON object per line: `h` (the first line, with the size), `o` (output; `d` is base64), `r` (a resize), `e` (the end, with the reason). `t` is milliseconds since the first line
- Only output and sizes are recorded, never input. But the output includes whatever was on screen (tokens, source code)
- A file stops at 64 MiB (`e` with `why: limit`). Files older than 14 days are deleted when the next recording starts
- If writing into the sandbox falls behind, the recording stops and the terminal goes on. If the recording cannot start, the terminal still opens (the log says why)
- Problems from before the recording started cannot be seen. A resize may be a few outputs off from where the screen applied it
- To replay a recording, run `go run ./cmd/term-replay [-at MS] [-hist] FILE` in a checkout of this repository (also inside a sandbox). It feeds the recording through Iwato's own emulator (`internal/screen`) and prints when the alternate screen was entered and left, the resizes, and the last screen. If the same breakage shows, the emulator is the cause; if not, the frames or the browser's drawing are
