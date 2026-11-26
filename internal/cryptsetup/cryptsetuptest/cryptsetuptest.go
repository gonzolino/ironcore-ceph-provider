// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

// Package cryptsetuptest creates LUKS headers for tests. Go allows no cgo
// in test files, so the calls live here.
package cryptsetuptest

/*
#cgo LDFLAGS: -lcryptsetup
#include <stdlib.h>
#include <string.h>
#include <libcryptsetup.h>

// A LUKS2 header with one keyslot whose key derivation is cheap.
static int cs_format_luks2(const char *path, const char *passphrase,
			   size_t passphrase_size) {
	struct crypt_device *cd = NULL;
	struct crypt_params_luks2 params;
	struct crypt_pbkdf_type pbkdf;
	int r;

	r = crypt_init(&cd, path);
	if (r < 0)
		return r;

	memset(&pbkdf, 0, sizeof(pbkdf));
	pbkdf.type = CRYPT_KDF_PBKDF2;
	pbkdf.hash = "sha256";
	pbkdf.iterations = 1000;
	pbkdf.time_ms = 1;
	pbkdf.flags = CRYPT_PBKDF_NO_BENCHMARK;
	r = crypt_set_pbkdf_type(cd, &pbkdf);

	memset(&params, 0, sizeof(params));
	if (r == 0)
		r = crypt_format(cd, CRYPT_LUKS2, "aes", "xts-plain64", NULL,
				 NULL, 64, &params);
	if (r == 0)
		r = crypt_keyslot_add_by_volume_key(cd, CRYPT_ANY_SLOT, NULL,
						    0, passphrase,
						    passphrase_size);
	crypt_free(cd);
	return r < 0 ? r : 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// fileSize leaves room for a data area behind the header of 16 MiB.
const fileSize = 32 << 20

// NewLUKS2File creates a sparse file at path with a LUKS2 header and one
// keyslot for passphrase.
func NewLUKS2File(path string, passphrase []byte) error {
	// libcryptsetup grows the file to the size of the header
	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		return err
	}

	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	cPassphrase := C.CBytes(passphrase)
	defer C.free(cPassphrase)

	r := C.cs_format_luks2(cPath, (*C.char)(cPassphrase),
		C.size_t(len(passphrase)))
	if r < 0 {
		return fmt.Errorf("format %s: %w", path, syscall.Errno(-r))
	}
	return os.Truncate(path, fileSize)
}
