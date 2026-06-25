//go:build windows

package store

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const dpapiPrefix = "dp1:"

func EncryptPassword(_ string, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	protected, err := dpapiProtect([]byte(plain))
	if err != nil {
		return "", err
	}
	return dpapiPrefix + base64.StdEncoding.EncodeToString(protected), nil
}

func DecryptPassword(dataDir, encoded string) (string, error) {
	_ = dataDir
	if encoded == "" {
		return "", nil
	}
	if strings.HasPrefix(encoded, dpapiPrefix) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, dpapiPrefix))
		if err != nil {
			return "", err
		}
		plain, err := dpapiUnprotect(raw)
		if err != nil {
			return "", err
		}
		return string(plain), nil
	}
	return "", fmt.Errorf("unsupported password format")
}

func dpapiProtect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	var in windows.DataBlob
	in.Size = uint32(len(data))
	in.Data = &data[0]
	var out windows.DataBlob
	err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := make([]byte, out.Size)
	copy(result, unsafe.Slice(out.Data, out.Size))
	return result, nil
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	var in windows.DataBlob
	in.Size = uint32(len(data))
	in.Data = &data[0]
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := make([]byte, out.Size)
	copy(result, unsafe.Slice(out.Data, out.Size))
	return result, nil
}
