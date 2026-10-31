# Sandboxes and images

[日本語](ja/sandboxes.md)

Each Iwato sandbox is one [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) sandbox (same name), running as containers in WSLc. Iwato runs OpenShell's gateway in WSLc and brings its own WSLc compute driver, which behaves like OpenShell's Docker driver.

## Create, start, stop

Create a sandbox with **Create a sandbox** in the sidebar:

- **Name**: a lowercase letter first, then lowercase letters, digits and hyphens, up to 32 characters. It also names the sandbox's published ports (`5173.NAME.localhost`), so a name that is not a valid DNS label gets no local port links ([publishing](publishing.md))
- **Image**: empty for Iwato's standard image (below), or any image, as with `openshell sandbox create --from` (for example `ubuntu:24.04`)
- **Use the GPU**: passes this PC's GPU into the sandbox (WSL's GPU; no choice of model or count). It can be changed later by **Recreate**, and appears only where a GPU is available. See [desktop and GPU](desktop.md)
- **Network presets**: the first network rules. Packages, Tool installs and Agent CLIs start checked; Reading references does not. Details shows what each allows ([network policy](network-policy.md#presets))
- **Providers**: credentials to attach ([providers](providers.md))

A new sandbox starts and connects its terminal. The sandbox's card has **Start** and a menu (⋯) with **Stop**, **Delete**, **Recreate** and **Providers**.

The card's tabs are **Terminal**, **Policy**, **Editor** and **Ports**, with the ⋯ menu at the end of the row. The menu opens the other views, **Links**, **Text**, **Files** and **Resources**, followed by the actions; the open view shows as a tab. While the agent's proposals wait, a line above the view shows them, with **Review** (the approval, or the policy tab when several wait) and **Later** (hidden until a new one arrives). See [terminals](terminals.md), [network policy](network-policy.md), [files and editor](files-and-editor.md) and [publishing](publishing.md).

## Resources

The card's header shows the sandbox's CPU (a percentage of its limit, or of the PC's cores without one), its memory against its limit and, with a GPU, the GPU's memory; a stopped sandbox shows its home's size as last measured. Values turn amber and red at the percentages in the settings. Clicking them opens **Resources**: CPU and memory over the last 10 minutes (only while the page is open), the memory's used, cache and free parts, the home's largest folders (measured every 10 minutes while the sandbox runs, or with **Measure again**) and the GPU's memory. On WSL the GPU's numbers are the whole GPU's, other sandboxes and Windows apps included. The sidebar shows the running sandboxes' total against the PC; **Volumes** shows each home's size.

## Stopping when the terminals end

A sandbox stops as soon as its last terminal exits. Stopping also ends processes outside the terminals (background dev servers, the desktop), so keep a terminal open while you need them. The home stays, and Start resumes it.

## Only the home persists

- The home `/home/dev` (`$HOME`) is always a volume. It survives deleting and recreating the sandbox
- Everything outside the home (the working directory `/sandbox`, `/usr/local`, `/var`, …) is kept while the sandbox is stopped and started, but is gone when the sandbox is deleted or recreated
- Work in `~/work`: terminals start there
- Agents, tools installed with mise, and their settings and logins live in the home, so they survive too. Tools that store data outside the home by default (a database under `/var/lib`) should be pointed at a directory under the home
- Creating a sandbox with the name of a deleted one picks up its home again
- **Volumes** in the sidebar lists the volumes per sandbox; volumes no sandbox uses can be deleted there

## Delete

**Delete** removes the sandbox and stops its processes, after you type its name. The home volume stays unless you also tick deleting it ("Also delete the home's data (volume)", cannot be undone).

## Recreate

A sandbox keeps the image it was created from. To move it to a newer image (for example a new standard image), use **Recreate**: Iwato deletes it and creates it again with the same name and settings from the current image, then starts it. This takes a few minutes and ends running processes and terminals.

- Kept: the home volume, the attached providers, the policy's rules and their history, and the creation settings (image, presets, GPU, publishing settings)
- Lost: files outside the home, and pending and rejected proposals (OpenShell keeps those per sandbox)
- Rules OpenShell no longer accepts are not restored; Iwato tells you which
- "Use the GPU" turns the GPU on or off for the new sandbox (shown where a GPU is available, or while the sandbox has one)
- Tick "Pull the latest image first" to fetch a newer standard image before recreating (development builds, see below)
- On Windows a sandbox also keeps the OpenShell supervisor (its network checks) it was created with. When Iwato brings a new one, the card shows **Recreate to update** (↻ in the sidebar); click it to open Recreate

## The standard image

Iwato's standard image (`ghcr.io/mtsmfm/iwato-sandbox`, built from `sandboximage/`) is not tied to any agent. The sandbox user `dev` is not root and has no sudo, so the image carries what is hard to install without root ([ADR 0004](../adr/0004-standard-image-contents.md)):

- Ubuntu 24.04 and common tools: git, gh (with a git credential helper for the GitHub provider), tmux, curl, ripgrep, fzf, zoxide, eza, bat, jq, python3 (the OS's) and more
- mise (with no tools chosen) and starship, with a shell setup based on Omarchy's bash defaults
- A build toolchain (build-essential, pkg-config, cmake, autoconf, automake, libtool, bison) and the development headers mise needs to build Ruby and Python and common native gems and wheels (OpenSSL, zlib, libyaml, libffi, readline, GMP, GDBM, SQLite, bzip2, liblzma, ncurses, uuid, libpq, MySQL client, libxml2, libxslt, libcurl)
- Clients and CLIs: sqlite3, postgresql-client, a MySQL client, unzip, zip, xz-utils, zstd, rsync, openssh-client, imagemagick, ffmpeg, procps, file, patch, tzdata, locales (en_US.UTF-8 and ja_JP.UTF-8)
- The system libraries Playwright's Chromium and Firefox need (install the browsers themselves into the home, for example `npx playwright install chromium`)
- A desktop (Xfce, Firefox, Selkies) and Mesa's d3d12 driver ([desktop and GPU](desktop.md))
- `/AGENTS.md`: instructions for agents about the environment (network and proposals, what persists, placeholder credentials, git). See [agents](agents.md)
- Iwato's sandbox convention (below)

No language runtimes (Node, Python, Go, Ruby, Java, …) and no agent CLIs are included: install them in the sandbox with mise ([agents](agents.md)).

### Package-manager defaults

Against malicious package releases, the image sets defaults for the package managers you install with mise ([ADR 0022](../adr/0022-iwato-owns-the-sandbox-policy.md)). They sit below the project's and your own settings, so either can change them:

| Tool | Default | Where | Change it |
|---|---|---|---|
| npm (11.10+) | `min-release-age=7` (days), `ignore-scripts=true` | `/etc/iwato/npmrc`, read as npm's global config through `NPM_CONFIG_GLOBALCONFIG` | the project's `.npmrc` or `~/.npmrc`; one command: `--min-release-age=0`, `--ignore-scripts=false` |
| uv (0.9.17+) | `exclude-newer = "7 days"` | `/etc/uv/uv.toml` | `[tool.uv]` or `uv.toml` of the project, `~/.config/uv/uv.toml`; one command: `--exclude-newer false` (uv 0.11.24+) |
| pip | none (pip 26.0 rejects a duration, and current Python ships it) | | with pip 26.1+, `[global] uploaded-prior-to = P7D` in `~/.config/pip/pip.conf` or the virtualenv's `pip.conf`; one command: `--uploaded-prior-to P7D` |
| Bundler (4.0.13+) | `BUNDLE_COOLDOWN=7` (days) | the shell's environment | `bundle config set --local cooldown N`; one command: `--cooldown 0` |

- Older versions ignore the setting; npm before 11.10 may warn that `min-release-age` is unknown
- A package that needs its install script (a native build) fails or stays unbuilt under npm: run that install with `--ignore-scripts=false`, or `npm rebuild PKG --ignore-scripts=false` afterwards. pnpm 10+ and Bun already run dependencies' scripts only for packages a project allows (Bun also for a list of popular ones); in Yarn Berry, `enableScripts: false` in `.yarnrc.yml` turns them off
- uv records the cooldown in `uv.lock`: in a project whose lockfile was made without one, `uv lock` rewrites it and `uv sync --locked` fails; `uv sync --frozen` installs the lockfile as it is. A project that does not want the cooldown sets `exclude-newer = false` in `[tool.uv]` (uv 0.11.24+)
- pnpm 11+ and recent Yarn Berry have a 1-day cooldown of their own (`minimumReleaseAge` in minutes, `npmMinimalAgeGate`); raise it in the project's or your global config (`~/.config/pnpm/config.yaml`, `~/.yarnrc.yml`). Bun has none by default, and its global `~/.bunfig.toml` is in the home, so the image cannot set it: set `[install] minimumReleaseAge = 604800` (seconds) there or in the project's `bunfig.toml`
- To drop the defaults for all your shells, add `unset NPM_CONFIG_GLOBALCONFIG BUNDLE_COOLDOWN` to `~/.bashrc` and set `exclude-newer = false` in `~/.config/uv/uv.toml` (uv 0.11.24+)
- `/AGENTS.md` asks agents to install from lockfiles (`npm ci`, `pnpm install --frozen-lockfile`, `uv sync --frozen`, `BUNDLE_FROZEN=true bundle install`, …)

### Where the image comes from

- A release exe pulls the image tagged with its own version, and an exe of the pre-release edge the image of its own commit; a new exe brings its image
- An exe you build yourself from main pulls the `edge` tag, which every merge to main publishes, but only when it has no copy. A newer `edge` is pulled only when you ask: **Update the standard image** in the settings, or the recreate dialog's **Pull the latest image first**. New and recreated sandboxes then use it; an old image is removed once no sandbox uses it, and when a pull fails the local image stays in use
- The settings show when the local image was created and pulled

## Your own images and the sandbox convention

Any image works. To get all of Iwato's features, an image follows Iwato's sandbox convention ([ADR 0005](../adr/0005-sandbox-image-convention.md)), as the standard image does:

- Iwato's daemon at `/usr/local/lib/iwato/iwato-entrypoint`, named by the image label `iwato.entrypoint` (built from the same commit as Iwato; it serves the terminals and SFTP of the home)
- The Policy Advisor guide in `/etc/openshell/skills/` and `/AGENTS.md`
- `WORKDIR` outside the home (`/sandbox`) and the user's home at `/home/dev`

In an image without the daemon, the main terminal is the sandbox's first process and extra terminals end when Iwato exits; files, the editor and image paste use OpenShell's SFTP, which shows the working directory instead of the home (the files tab and the editor note "Opens the working directory (…)"). When the image's daemon is older than Iwato, what it supports keeps working and the rest falls back the same way; recreating the sandbox brings the new daemon.
