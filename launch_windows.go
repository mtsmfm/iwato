//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procAssocQueryString    = windows.NewLazySystemDLL("shlwapi.dll").NewProc("AssocQueryStringW")
	procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
)

func openURL(u string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// protocolHandled: something is registered to open the protocol's URLs
// (Chrome and Edge register an installed app's protocol handlers).
func protocolHandled(protocol string) bool {
	const assocstrCommand = 1
	p, err := windows.UTF16PtrFromString(protocol)
	if err != nil {
		return false
	}
	verb, _ := windows.UTF16PtrFromString("open")
	var size uint32
	// With no buffer it answers S_FALSE and the command's length.
	r, _, _ := procAssocQueryString.Call(0, assocstrCommand, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(verb)), 0, uintptr(unsafe.Pointer(&size)))
	return (r == 0 || r == 1) && size > 1
}

// portOwner names the process listening on the IPv4 port, or "".
func portOwner(port int) string {
	const tcpTableOwnerPidListener = 3
	var buf []byte
	size := uint32(0)
	for range 3 {
		var p uintptr
		if len(buf) > 0 {
			p = uintptr(unsafe.Pointer(&buf[0]))
		}
		r, _, _ := procGetExtendedTcpTable.Call(p, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, tcpTableOwnerPidListener, 0)
		if r == 0 && len(buf) > 0 {
			break
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && r != 0 {
			return ""
		}
		buf = make([]byte, size+1024)
		size = uint32(len(buf))
	}
	if len(buf) < 4 {
		return ""
	}
	// MIB_TCPROW_OWNER_PID: state, local address, local port (network
	// order in the low 16 bits), remote address, remote port, PID.
	const row = 24
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n && 4+(i+1)*row <= len(buf); i++ {
		r := buf[4+i*row:]
		if int(binary.BigEndian.Uint16(r[8:10])) != port {
			continue
		}
		pid := binary.LittleEndian.Uint32(r[20:24])
		if name := processName(pid); name != "" {
			return fmt.Sprintf("%s (PID %d)", name, pid)
		}
		return fmt.Sprintf("PID %d", pid)
	}
	return ""
}

func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
