//go:build windows

package protect

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Scheme tells how the file is protected: DPAPI for the current user.
const Scheme = "dpapi"

// entropy ties the blobs to Iwato (another program running as the user
// must pass the same to decrypt them).
var entropy = []byte("iwato secret v1")

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func take(out *windows.DataBlob) []byte {
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

func protect(b []byte) ([]byte, error) {
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString("iwato")
	if err := windows.CryptProtectData(blob(b), name, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return take(&out), nil
}

func unprotect(b []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(b), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return take(&out), nil
}

// DeleteKey does nothing: DPAPI's key is the user's own.
func DeleteKey() error { return nil }
