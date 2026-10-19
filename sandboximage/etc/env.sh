# Environment variables Iwato writes for the terminals in the home
# (~/.config/iwato/env, rewritten when the sandbox starts and when its
# providers change). Read from /etc/profile.d and /etc/bash.bashrc, so
# before the owner's ~/.profile and ~/.bashrc, which may override them.
__iwato_env=${HOME:-}/.config/iwato/env
if [ -f "$__iwato_env" ] && [ ! -L "$__iwato_env" ] && [ -O "$__iwato_env" ]; then
  . "$__iwato_env"
fi
unset __iwato_env
