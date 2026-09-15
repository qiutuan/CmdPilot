//go:build windows

package secrets

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// appEntropy binds DPAPI blobs to this application so that other programs
// running as the same user still cannot decrypt our values without the entropy.
var appEntropy = []byte("CmdPilot::secrets::v1")

// dataBlob mirrors the Win32 CRYPTOAPI_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newDataBlob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// Protect encrypts plaintext with DPAPI (CRYPTPROTECT_UI_FORBIDDEN).
func Protect(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	var out dataBlob
	entropy := newDataBlob(appEntropy)
	in := newDataBlob([]byte(plaintext))
	if err := windows.CryptProtectData(in, nil, entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", fmt.Errorf("secrets: DPAPI protect failed: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.pbData, int(out.cbData))), nil
}

// Unprotect decrypts a DPAPI-protected value produced by Protect.
func Unprotect(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secrets: invalid base64 payload: %w", err)
	}
	var out dataBlob
	entropy := newDataBlob(appEntropy)
	in := newDataBlob(raw)
	if err := windows.CryptUnprotectData(in, nil, entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", fmt.Errorf("secrets: DPAPI unprotect failed: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return string(unsafe.Slice(out.pbData, int(out.cbData))), nil
}
