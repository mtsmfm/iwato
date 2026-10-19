# The image of Iwato's OpenShell sandboxes: a generic Ubuntu with the tools
# (git, gh and the credential helper for GitHub providers, tmux, ripgrep,
# ...), mise with no tools listed, starship and the shell setup, a desktop
# for GUI apps (Xfce, Firefox and Selkies, an HTML5 remote desktop; it is not
# started automatically: `iwato-desktop` starts it), and what a user without
# root cannot add with mise (docs/adr/0004-standard-image-contents.md): a C
# toolchain and the headers mise needs to build Ruby, Python and common native
# gems and wheels, database clients and CLIs, locales, and the system libraries
# Playwright's Chromium and Firefox need (not the browsers: users install them
# in their home). No language runtime (node, go, ruby, java): users install
# them with mise; python3 stays as part of the OS. Nothing in it is specific
# to an agent: no agent CLI and no agent settings. Users install their agent
# with mise (e.g. mise use -g claude); it lands in the home, which persists. The environment's instructions are /AGENTS.md, so
# OpenShell keeps them instead of installing its own. It links to
# /etc/iwato/AGENTS.md: the sandbox policy lets the workload read /etc, not
# files directly in /.
# The image follows Iwato's convention (docs/adr/0005-sandbox-image-
# convention.md): it carries Iwato's daemon (/usr/local/lib/iwato/
# iwato-entrypoint, the sandbox's main process: terminals, and SFTP rooted
# at the home for the files tab, the editor and image paste; the label
# iwato.entrypoint names it), OpenShell's Policy Advisor guide
# (/etc/openshell/skills) and /AGENTS.md; its WORKDIR (/sandbox) is outside
# the home.
# OpenShell runs the workload as the image's user (dev) with every capability
# dropped and no sudo, so OS packages belong here. Iwato sets HOME=/home/dev
# and keeps it on a volume: only what is under it outlives the sandbox.
# Terminals start in ~/work. Release exes
# pull this image from ghcr.io tagged with their version
# (.github/workflows/release.yml), other exes the `edge` tag main publishes
# (.github/workflows/image.yml); `scripts/build-exe.sh --local-images` builds
# it from the sources embedded in the exe.
#
# Layer order is for cache reuse: the heavy desktop layers (about a gigabyte
# of packages, Firefox, Selkies) come right after the base packages and
# depend on no file of this directory, so editing the development packages,
# the shell setup, the tools script or the desktop scripts below does not
# rebuild them. The desktop runs
# entirely as dev: OpenShell starts the workload as the image's user with
# every capability dropped and no sudo, so nothing needs root at run time (no
# s6 /init, no writes to /etc, no ldconfig). Root is used only here, to
# install. A failure in the desktop steps fails the whole build, with a
# message saying which step: do not make them optional.
FROM ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends bash bash-completion ca-certificates git gh tmux curl ripgrep less fzf zoxide eza bat jq python3 brotli \
    && rm -rf /var/lib/apt/lists/* \
    && (if getent passwd ubuntu >/dev/null; then userdel -r ubuntu; fi) \
    && (if getent group 1000 >/dev/null; then groupmod -n dev "$(getent group 1000 | cut -d: -f1)"; else groupadd --gid 1000 dev; fi) \
    && useradd --home-dir /home/dev --no-create-home --uid 1000 --gid 1000 --shell /bin/bash dev \
    && usermod --password '*' dev \
    && install -d -o dev -g dev -m 700 /home/dev \
    && (command -v bat >/dev/null || ln -s /usr/bin/batcat /usr/local/bin/bat)
# --- the desktop -------------------------------------------------------------
# Selkies' native package (https://docs.selkies.io/native): a private Python
# environment in /opt/selkies with selkies, selkies-session and its libraries.
# 2.0.0 is the release linuxserver/docker-baseimage-selkies pins. The
# package's SHA-256 per architecture is pinned below (from the GitHub release's
# asset digests, docs/dev/development.md#bumping-pinned-versions): a mismatch,
# or a version or architecture with no hash, fails the build.
# SELKIES_DEB_SHA256 overrides the pinned hash (for another SELKIES_VERSION).
ARG SELKIES_VERSION=2.0.0
ARG SELKIES_DEB_SHA256=
# Mesa's d3d12 driver and VA-API for the WSL GPU; Xvfb, PulseAudio, D-Bus and
# Xfce for the session; CJK and emoji fonts.
RUN apt-get update && apt-get install -y --no-install-recommends \
        xfce4 xfce4-terminal dbus-x11 dbus-bin xvfb pulseaudio pulseaudio-utils x11-utils x11-xserver-utils xclip \
        adwaita-icon-theme fonts-noto-cjk fonts-noto-color-emoji gnupg \
        libgl1-mesa-dri mesa-va-drivers mesa-utils vainfo \
    && rm -rf /var/lib/apt/lists/*
# Firefox from Mozilla's apt repository (Ubuntu's firefox package is a snap
# stub). The key's fingerprint is checked against the one Mozilla publishes
# (a mismatch stops the build and prints the fingerprint it found).
RUN install -d -m 755 /etc/apt/keyrings \
    && curl -fsSL --retry 3 -o /etc/apt/keyrings/packages.mozilla.org.asc https://packages.mozilla.org/apt/repo-signing-key.gpg \
        || { echo "desktop: cannot download Mozilla's apt signing key" >&2; exit 1; }; \
    fpr="$(gpg --quiet --show-keys --with-colons /etc/apt/keyrings/packages.mozilla.org.asc | awk -F: '/^fpr:/ {print $10; exit}')"; \
    [ "$fpr" = 35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3 ] \
        || { echo "desktop: Mozilla apt key fingerprint is '$fpr', expected 35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3 (update the Dockerfile only after checking it at https://support.mozilla.org/kb/install-firefox-linux)" >&2; exit 1; }; \
    echo 'deb [signed-by=/etc/apt/keyrings/packages.mozilla.org.asc] https://packages.mozilla.org/apt mozilla main' > /etc/apt/sources.list.d/mozilla.list \
    && printf 'Package: *\nPin: origin packages.mozilla.org\nPin-Priority: 1000\n' > /etc/apt/preferences.d/mozilla \
    && apt-get update && apt-get install -y --no-install-recommends firefox \
    && rm -rf /var/lib/apt/lists/* \
    && command -v firefox >/dev/null
# The default browser. Mozilla's package registers no Debian alternatives and
# Ubuntu's /etc/xdg/xfce4/helpers.rc says WebBrowser=debian-sensible-browser,
# which runs $BROWSER and then /usr/bin/x-www-browser (none here): clicking a
# link in Xfce then fails with "Failed to execute default Web Browser". So:
#  - x-www-browser and gnome-www-browser point at Firefox;
#  - Xfce's preferred browser (xfce4-mime-helper, exo-open --launch WebBrowser)
#    is the "firefox" helper (xfce4-helpers ships helpers/firefox.desktop);
#  - /etc/xdg/mimeapps.list makes Firefox's desktop file the handler of http,
#    https and HTML (what xdg-open, GIO and xdg-settings read).
# The desktop file is looked up, not assumed, and a missing one fails the build.
RUN set -e; \
    update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/bin/firefox 200; \
    update-alternatives --install /usr/bin/gnome-www-browser gnome-www-browser /usr/bin/firefox 200; \
    update-alternatives --set x-www-browser /usr/bin/firefox; \
    update-alternatives --set gnome-www-browser /usr/bin/firefox; \
    [ "$(readlink -f /usr/bin/x-www-browser)" = "$(readlink -f /usr/bin/firefox)" ] \
        || { echo "desktop: x-www-browser does not lead to Firefox" >&2; exit 1; }; \
    [ -f /usr/share/xfce4/helpers/firefox.desktop ] \
        || { echo "desktop: no /usr/share/xfce4/helpers/firefox.desktop (xfce4-helpers); Xfce cannot be told to use Firefox" >&2; exit 1; }; \
    ffdesktop="$(ls /usr/share/applications 2>/dev/null | grep -i '^firefox.*\.desktop$' | head -n 1 || true)"; \
    [ -n "$ffdesktop" ] \
        || { echo "desktop: Mozilla's package installed no firefox*.desktop in /usr/share/applications" >&2; exit 1; }; \
    install -d -m 755 /etc/xdg/xfce4; \
    printf '# Written by the Iwato image: Firefox is the preferred browser.\nWebBrowser=firefox\nMailReader=thunderbird\nTerminalEmulator=debian-x-terminal-emulator\nFileManager=thunar\n' > /etc/xdg/xfce4/helpers.rc; \
    printf '[Default Applications]\nx-scheme-handler/http=%s\nx-scheme-handler/https=%s\ntext/html=%s\napplication/xhtml+xml=%s\n' "$ffdesktop" "$ffdesktop" "$ffdesktop" "$ffdesktop" > /etc/xdg/mimeapps.list; \
    chmod 644 /etc/xdg/xfce4/helpers.rc /etc/xdg/mimeapps.list
RUN arch="$(dpkg --print-architecture)"; \
    url="https://github.com/selkies-project/selkies/releases/download/${SELKIES_VERSION}/selkies-${SELKIES_VERSION}-ubuntu24.04-${arch}.deb"; \
    curl -fsSL --retry 3 -o /tmp/selkies.deb "$url" \
        || { echo "desktop: cannot download the Selkies package $url (check the release's assets, then SELKIES_VERSION and this URL)" >&2; exit 1; }; \
    want="$SELKIES_DEB_SHA256"; \
    [ -n "$want" ] || case "$SELKIES_VERSION-$arch" in \
        2.0.0-amd64) want=bbaa4d71012b9374a753b7dfddc1da07e31f34b04277fe4fb3d045f18fd88391 ;; \
        2.0.0-arm64) want=3900f3ba805898c495829629092553cc1cf4d5a864ffc4056f57d21646ad45e4 ;; \
    esac; \
    sum="$(sha256sum /tmp/selkies.deb | cut -d' ' -f1)"; \
    [ -n "$want" ] || { echo "desktop: no SHA-256 pinned for selkies $SELKIES_VERSION ($arch); its SHA-256 is $sum (pin it in sandboximage/Dockerfile after checking it against the release)" >&2; exit 1; }; \
    [ "$sum" = "$want" ] || { echo "desktop: selkies .deb ($arch) is $sum, expected $want" >&2; exit 1; }; \
    apt-get update && apt-get install -y --no-install-recommends /tmp/selkies.deb \
        || { echo "desktop: cannot install the Selkies package" >&2; exit 1; }; \
    rm -rf /tmp/selkies.deb /var/lib/apt/lists/*; \
    command -v selkies-session >/dev/null \
        || { echo "desktop: the Selkies package installed no selkies-session on PATH (is it in /opt/selkies/bin?)" >&2; exit 1; }
# --- development packages ----------------------------------------------------
# One layer, after the desktop so that changing the list does not rebuild it.
#  - the toolchain (build-essential: gcc, g++, make, libc6-dev) and build tools;
#  - the headers mise needs to build Ruby (ruby-build: libssl, libyaml, zlib,
#    libffi, readline, gmp, gdbm) and Python from source (sqlite3, bz2, lzma,
#    ncurses, uuid; no tk-dev: tkinter is rarely needed in a sandbox and it
#    pulls the X11 headers), and those of common native gems and wheels (pg,
#    mysql2, nokogiri, curl). No libvips: sharp ships its own and it is large;
#    ImageMagick is here;
#  - database clients, archivers and CLIs (procps for ps and top, which the
#    base image lacks);
#  - the system libraries of Playwright's Chromium and Firefox, as
#    `npx playwright install-deps chromium firefox` lists them for Ubuntu
#    24.04 (from Playwright's nativeDeps, not yet checked against
#    `playwright install-deps`; xvfb and the CJK and emoji fonts are already
#    in the desktop layer);
#  - locales: en_US.UTF-8 and ja_JP.UTF-8 are generated (C.UTF-8 is built in).
# The toolchain also builds the desktop's setuid shim below.
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config cmake autoconf automake libtool bison make patch file \
        libssl-dev zlib1g-dev libyaml-dev libffi-dev libreadline-dev libgmp-dev libgdbm-dev libgdbm-compat-dev \
        libsqlite3-dev libbz2-dev liblzma-dev libncurses-dev uuid-dev \
        libpq-dev default-libmysqlclient-dev libxml2-dev libxslt1-dev libcurl4-openssl-dev \
        sqlite3 postgresql-client default-mysql-client \
        unzip zip xz-utils zstd rsync openssh-client imagemagick ffmpeg procps locales tzdata \
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 libcairo-gobject2 libcups2t64 \
        libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libgdk-pixbuf-2.0-0 libgtk-3-0t64 libnspr4 libnss3 \
        libpango-1.0-0 libpangocairo-1.0-0 libfontconfig1 libfreetype6 libx11-6 libx11-xcb1 libxcb1 libxcb-shm0 \
        libxcomposite1 libxcursor1 libxdamage1 libxext6 libxfixes3 libxi6 libxkbcommon0 libxrandr2 libxrender1 \
        fonts-liberation \
    && rm -rf /var/lib/apt/lists/* \
    && locale-gen en_US.UTF-8 ja_JP.UTF-8 \
    && locale -a | grep -qix 'ja_JP.utf8' && locale -a | grep -qix 'en_US.utf8' \
    && for c in gcc g++ make cmake pkg-config autoconf sqlite3 psql mysql convert ffmpeg zstd rsync ssh unzip ps; do \
        command -v "$c" >/dev/null || { echo "development packages: no $c on PATH" >&2; exit 1; }; done
COPY install-tools.sh /tmp/install-tools.sh
RUN sh /tmp/install-tools.sh && rm /tmp/install-tools.sh
COPY etc/ /etc/iwato/
COPY AGENTS.md /etc/iwato/AGENTS.md
# OpenShell's Policy Advisor guide, which its supervisor means to install but
# writes into its own container (NVIDIA/OpenShell#3698).
COPY openshell-skills/ /etc/openshell/skills/
# The GPU settings must reach exec'd terminals (the image's ENV does not), so
# login shells and interactive shells read them; iwato-desktop sources the
# same file. They are set only where /dev/dxg exists, so there is no ENV.
COPY desktop/gpu-env.sh /etc/iwato/desktop/gpu-env.sh
COPY desktop/iwato-desktop /usr/local/bin/iwato-desktop
COPY desktop/iwato-desktop-doctor /usr/local/bin/iwato-desktop-doctor
# The seccomp filter of OpenShell's sandboxes makes setuid(getuid()) and
# setgid(getgid()) fail with EPERM, which kills the child Xvfb starts xkbcomp
# with ("XKB: Failed to compile keymap"). iwato-desktop preloads this shim,
# which makes those calls succeed when the id does not change. It is built
# with the toolchain of the development packages above.
COPY desktop/iwato-noid.c /tmp/iwato-noid.c
RUN install -d -m 755 /usr/local/lib/iwato \
    && { gcc -shared -fPIC -O2 -nostdlib -o /usr/local/lib/iwato/libiwato-noid.so /tmp/iwato-noid.c \
        && test -s /usr/local/lib/iwato/libiwato-noid.so \
        || { echo "desktop: cannot build the setuid shim (iwato-noid.c)" >&2; exit 1; }; } \
    && rm -f /tmp/iwato-noid.c \
    && command -v Xvfb >/dev/null && command -v xkbcomp >/dev/null \
    && command -v selkies-session >/dev/null && command -v xfce4-session >/dev/null
RUN ln -s /etc/iwato/AGENTS.md /AGENTS.md \
    && ln -s /etc/iwato/gitconfig /etc/gitconfig && ln -s /etc/iwato/tmux.conf /etc/tmux.conf \
    && install -m 755 /etc/iwato/iwato-browser /usr/local/bin/iwato-browser \
    && chmod 755 /usr/local/bin/iwato-desktop /usr/local/bin/iwato-desktop-doctor && chmod -R a+rX /etc/iwato /etc/openshell \
    && ln -s /etc/iwato/desktop/gpu-env.sh /etc/profile.d/iwato-gpu.sh \
    && echo '[ -r /etc/iwato/desktop/gpu-env.sh ] && . /etc/iwato/desktop/gpu-env.sh' >> /etc/bash.bashrc \
    && ln -s /etc/iwato/env.sh /etc/profile.d/iwato-env.sh \
    && echo '[ -r /etc/iwato/env.sh ] && . /etc/iwato/env.sh' >> /etc/bash.bashrc
# The home (/home/dev) is a volume Iwato mounts over the image's, so nothing
# made in it here would be seen: a shell without ~/.bashrc gets the defaults
# from here, and the owner's ~/.bashrc replaces them.
RUN echo '[ -e "$HOME/.bashrc" ] || { [ -r /etc/iwato/shell/bashrc ] && . /etc/iwato/shell/bashrc; }' >> /etc/bash.bashrc
# Iwato's daemon, built from the same commit as Iwato (cmd/iwato-entrypoint)
# into _entrypoint/ for each architecture: by scripts/build-entrypoint.sh for
# the image's workflows, by the exe for a local build (--local-images). Last,
# since it changes with Iwato more often than anything above.
COPY _entrypoint/ /tmp/iwato-entrypoint/
RUN arch="$(dpkg --print-architecture)" \
    && case "$arch" in amd64) elf=x86-64 ;; arm64) elf=aarch64 ;; *) elf=none ;; esac \
    && install -m 0555 "/tmp/iwato-entrypoint/iwato-entrypoint-$arch" /usr/local/lib/iwato/iwato-entrypoint \
    && rm -rf /tmp/iwato-entrypoint \
    && file -b /usr/local/lib/iwato/iwato-entrypoint | grep -q "ELF 64-bit.*$elf" \
    || { echo "iwato-entrypoint: no binary for $arch in _entrypoint/ (scripts/build-entrypoint.sh)" >&2; exit 1; }
LABEL iwato.entrypoint=/usr/local/lib/iwato/iwato-entrypoint
# The workspace (OpenShell's working directory) is outside the home, so that
# the home can be a volume within OpenShell's rules (a volume cannot cover
# the working directory or its parents). It is not kept: work happens in
# ~/work.
RUN install -d -o dev -g dev -m 700 /sandbox
USER dev
WORKDIR /sandbox
CMD ["/bin/bash", "-l"]
