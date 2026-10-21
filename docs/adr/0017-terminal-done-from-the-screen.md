# 0017. A terminal is "done" by what its screen shows, not by the owner's doing

- Status: Accepted
- Date: 2026-10-21

## Context

Iwato pushes when a terminal stopped changing (an agent finished). It compared each terminal's screen every 15 seconds against the screen when the owner left: any change made it active, a minute without one pushed. Changes the owner caused themselves pushed too: opening a terminal, scrolling (a TUI redraws, as wheel events are input to it), resizing, attaching and detaching. So did one-off redraws, such as Claude Code's recap after it has been idle.

Alternatives not chosen:

- The agents' hooks (Claude Code's `Notification`/`Stop`): every tool has its own, they must be set up in each sandbox, and other programs have none
- Making Iwato pass for another terminal (`TERM_PROGRAM` set to iTerm2 and the like) so tools send their notifications: it changes how programs behave elsewhere, and Iwato would have to follow those terminals

## Decision

The judgement stays with the screen; the tools' own signals are honoured where a terminal standard carries them.

- Owner's input does not count: changes within 5 seconds after any client input (keys, paste, mouse and wheel, resize, attach, detach) are the owner's doing
- Work is a screen that keeps changing: changes in at least 3 polls (every 5 seconds) spanning at least 20 seconds. A single burst is not work
- Done is work followed by a minute without changes. Rows that keep changing alone (timers, spinners, token counts) are volatile and do not count as changes
- What the owner saw: the screen when they last left is the baseline. When the screen at done equals it apart from volatile rows, nothing is pushed
- Bells and notifications from programs (BEL, OSC 9, OSC 777 `notify`, OSC 99) push at once, outside the owner's input windows, at most one per terminal per minute
- Known agents' working markers (Claude Code's and Codex's "esc to interrupt") hold back "done" while they show, and their going away after work is done at once. This is a hint in one table, not a requirement

The page's stopped marks follow the same judgement.

## Consequences

- Viewing, scrolling and closing a terminal no longer push; an agent that works and stops still does
- A program that only ticks a timer while it works and shows no known marker may be taken as done after a minute
- Output shorter than 20 seconds is not pushed unless the program rings the bell; Claude Code's "Terminal bell" notifications make that prompt
