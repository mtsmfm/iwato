# Sandbox shell (Omarchy defaults)

Bash setup adapted from [Omarchy](https://github.com/basecamp/omarchy) `default/bash` at
commit a85e29a (MIT, Copyright (c) David Heinemeier Hansson; the license is in `LICENSE`).
`inputrc` (from `default/bash/inputrc`) and `starship.toml` (from `config/starship.toml`)
are verbatim; `envs`, `init` and `aliases` drop what needs the Omarchy
desktop (launchers, herdr, tdl, nvim, docker). `shell` also appends each command line
to `~/.bash_history` before it runs (PS0 marks the line, a DEBUG trap runs
`history -a`) and at each prompt: terminals are killed with the container, never
exited cleanly, so a command still running then (an agent CLI, a dev server) would
otherwise be lost. Installed at `/etc/iwato/shell`.

A shell without `~/.bashrc` reads `bashrc` (through `/etc/bash.bashrc`), which sources `rc`.
To add your own settings, copy it to `~/.bashrc` (the home is a volume, so the image
cannot make one) and write below the `source` line.
