# 0018. A terminal that keeps ticking is not done

- Status: Accepted
- Date: 2026-10-21

## Context

ADR 0017 compares rows by their letters and ignores rows that change alone, so a program that shows its progress only as numbers, a spinner or a timer (`Building… 45%`, a test run's elapsed time, an agent thinking) is taken as done a minute after its letters stop changing, while it still works.

## Decision

This amends ADR 0017; the rest of it stays.

- At each poll (every 5 seconds), whether the screen as shown changed at all is recorded: digits, spinner glyphs and volatile rows included, changes within the owner's input windows left out
- A terminal whose screen changed in at least 3 of the last 4 polls is alive. An alive terminal is not done; the minute without changes starts at its last change once it stops ticking. A known agent's working hint going away is still done at once
- Slow tickers (a clock that changes once a minute, about 1 poll in 12) are not alive

## Consequences

- Long builds, test runs and agents that show only a counter or a spinner are no longer reported done while they work
- A program that keeps a spinner or a timer running while it waits for input is never reported done (its bell still is). Claude Code stops its spinner when it waits
