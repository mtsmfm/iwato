# GPU settings for shells and the desktop (sourced; POSIX sh and bash).
# The image's ENV does not reach exec'd terminals, so the Dockerfile links
# this file into /etc/profile.d and /etc/bash.bashrc. Nothing is set without
# /dev/dxg (the WSL GPU): Mesa's d3d12 driver cannot create a screen then, and
# whether it falls back to llvmpipe when GALLIUM_DRIVER names it is not
# something this image has been shown to do.
if [ -e "${IWATO_DXG:-/dev/dxg}" ]; then
  export GALLIUM_DRIVER=d3d12 LIBVA_DRIVER_NAME=d3d12
  # iwato-desktop links the NVIDIA driver store's full libcuda here.
  iwato_nv="${IWATO_NVIDIA_LIBS:-${HOME:-/sandbox}/.local/lib/wsl-nvidia}"
  if [ -e "$iwato_nv/libcuda.so.1" ]; then
    case ":${LD_LIBRARY_PATH:-}:" in
      *":$iwato_nv:"*) ;;
      *) export LD_LIBRARY_PATH="$iwato_nv${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ;;
    esac
  fi
  unset iwato_nv
fi
