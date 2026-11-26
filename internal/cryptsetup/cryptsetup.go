// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

// Package cryptsetup wraps the calls of libcryptsetup that change the
// keyslots of a LUKS header. It works on a header in a file and never
// activates a device.
package cryptsetup

/*
#cgo LDFLAGS: -lcryptsetup
#include <stdlib.h>
#include <string.h>
#include <libcryptsetup.h>

// The last error message of a handle. Without a callback libcryptsetup
// prints its messages to stdout and stderr.
struct cs_log {
	char msg[512];
};

static void cs_log_cb(int level, const char *msg, void *usrptr) {
	struct cs_log *log = usrptr;
	size_t n;

	if (level != CRYPT_LOG_ERROR || log == NULL || msg == NULL)
		return;
	strncpy(log->msg, msg, sizeof(log->msg) - 1);
	log->msg[sizeof(log->msg) - 1] = '\0';
	n = strlen(log->msg);
	while (n > 0 && log->msg[n - 1] == '\n')
		log->msg[--n] = '\0';
}

static void cs_set_log(struct crypt_device *cd, struct cs_log *log) {
	crypt_set_log_callback(cd, cs_log_cb, log);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// AnyKeyslot leaves the choice of the keyslot to libcryptsetup.
const AnyKeyslot = int(C.CRYPT_ANY_SLOT)

// Key derivation functions of a keyslot.
const (
	Argon2id = "argon2id"
	Argon2i  = "argon2i"
	PBKDF2   = "pbkdf2"
)

var (
	// ErrClosed is returned by calls on a Device that was closed.
	ErrClosed = errors.New("cryptsetup: device is closed")
	// ErrEmptyPassphrase is returned for a passphrase without bytes.
	ErrEmptyPassphrase = errors.New("cryptsetup: passphrase is empty")
	// ErrLastKeyslot is returned by RemoveKeyslot for the last keyslot in
	// use. Without it nothing opens the volume any more.
	ErrLastKeyslot = errors.New("cryptsetup: refusing to remove the last keyslot in use")
)

// PBKDF is the key derivation of a keyslot and its cost.
type PBKDF struct {
	// Type is Argon2id, Argon2i or PBKDF2.
	Type string
	// Hash is the hash of PBKDF2. Empty for the others.
	Hash string
	// Iterations is the time cost.
	Iterations uint32
	// MemoryKiB is the memory cost. Zero for PBKDF2.
	MemoryKiB uint32
	// Threads is the parallel cost. Zero for PBKDF2.
	Threads uint32
}

// Keyslot is a keyslot in use.
type Keyslot struct {
	ID    int
	PBKDF PBKDF
}

// Device is a libcryptsetup handle on a LUKS header. It must not be used
// from two goroutines at once.
type Device struct {
	cd  *C.struct_crypt_device
	log *C.struct_cs_log
}

// Open loads the LUKS header at path, which can be a file, a block device
// or /proc/self/fd/<fd>.
func Open(path string) (*Device, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var cd *C.struct_crypt_device
	if r := C.crypt_init(&cd, cPath); r < 0 {
		return nil, fmt.Errorf("crypt_init: %w", syscall.Errno(-r))
	}
	log := (*C.struct_cs_log)(C.calloc(1, C.sizeof_struct_cs_log))
	if log == nil {
		C.crypt_free(cd)
		return nil, fmt.Errorf("crypt_init: %w", syscall.ENOMEM)
	}
	d := &Device{cd: cd, log: log}
	C.cs_set_log(d.cd, d.log)

	// a nil type is CRYPT_LUKS, any LUKS version
	if r := C.crypt_load(d.cd, nil, nil); r < 0 {
		err := d.fail("crypt_load", r)
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close frees the handle. Changes are in the file as soon as the call that
// made them returns, Close writes nothing.
func (d *Device) Close() {
	if d.cd == nil {
		return
	}
	C.crypt_free(d.cd)
	d.cd = nil
	C.free(unsafe.Pointer(d.log))
	d.log = nil
}

// Type returns the format of the header, "LUKS1" or "LUKS2".
func (d *Device) Type() string {
	if d.cd == nil {
		return ""
	}
	return C.GoString(C.crypt_get_type(d.cd))
}

// Keyslots lists the keyslots in use, in ascending order.
func (d *Device) Keyslots() ([]Keyslot, error) {
	if d.cd == nil {
		return nil, ErrClosed
	}
	d.log.msg[0] = 0

	count := C.crypt_keyslot_max(C.crypt_get_type(d.cd))
	if count < 0 {
		return nil, d.fail("crypt_keyslot_max", count)
	}

	var keyslots []Keyslot
	for id := C.int(0); id < count; id++ {
		status := C.crypt_keyslot_status(d.cd, id)
		if status != C.CRYPT_SLOT_ACTIVE && status != C.CRYPT_SLOT_ACTIVE_LAST {
			continue
		}
		var pbkdf C.struct_crypt_pbkdf_type
		if r := C.crypt_keyslot_get_pbkdf(d.cd, id, &pbkdf); r < 0 {
			return nil, d.fail("crypt_keyslot_get_pbkdf", r)
		}
		keyslots = append(keyslots, Keyslot{
			ID: int(id),
			PBKDF: PBKDF{
				Type:       C.GoString(pbkdf._type),
				Hash:       C.GoString(pbkdf.hash),
				Iterations: uint32(pbkdf.iterations),
				MemoryKiB:  uint32(pbkdf.max_memory_kb),
				Threads:    uint32(pbkdf.parallel_threads),
			},
		})
	}
	return keyslots, nil
}

// SetPBKDF sets the key derivation of the keyslots that this handle
// creates from now on. The cost is taken as given: libcryptsetup does not
// measure the host, so the keyslot costs the same wherever it is created.
func (d *Device) SetPBKDF(pbkdf PBKDF) error {
	if d.cd == nil {
		return ErrClosed
	}
	d.log.msg[0] = 0

	// libcryptsetup keeps copies of the strings
	cType := C.CString(pbkdf.Type)
	defer C.free(unsafe.Pointer(cType))
	var cHash *C.char
	if pbkdf.Hash != "" {
		cHash = C.CString(pbkdf.Hash)
		defer C.free(unsafe.Pointer(cHash))
	}

	cPBKDF := C.struct_crypt_pbkdf_type{
		_type:            cType,
		hash:             cHash,
		iterations:       C.uint32_t(pbkdf.Iterations),
		max_memory_kb:    C.uint32_t(pbkdf.MemoryKiB),
		parallel_threads: C.uint32_t(pbkdf.Threads),
		flags:            C.CRYPT_PBKDF_NO_BENCHMARK,
	}
	if r := C.crypt_set_pbkdf_type(d.cd, &cPBKDF); r < 0 {
		return d.fail("crypt_set_pbkdf_type", r)
	}
	return nil
}

// AddKeyslot stores newPassphrase in a free keyslot and returns the
// keyslot. passphrase has to open a keyslot in use. keyslot names the
// keyslot to fill, AnyKeyslot takes the first free one.
func (d *Device) AddKeyslot(keyslot int, passphrase, newPassphrase []byte) (int, error) {
	if d.cd == nil {
		return 0, ErrClosed
	}
	d.log.msg[0] = 0

	cPassphrase, err := newSecret(passphrase)
	if err != nil {
		return 0, err
	}
	defer C.crypt_safe_free(cPassphrase)
	cNewPassphrase, err := newSecret(newPassphrase)
	if err != nil {
		return 0, err
	}
	defer C.crypt_safe_free(cNewPassphrase)

	r := C.crypt_keyslot_add_by_passphrase(d.cd, C.int(keyslot),
		(*C.char)(cPassphrase), C.size_t(len(passphrase)),
		(*C.char)(cNewPassphrase), C.size_t(len(newPassphrase)))
	if r < 0 {
		return 0, d.fail("crypt_keyslot_add_by_passphrase", r)
	}
	return int(r), nil
}

// CheckPassphrase returns the keyslot that passphrase opens. keyslot
// limits the check to one keyslot, AnyKeyslot tries all in use. A
// passphrase that opens none is syscall.EPERM. Nothing is activated.
func (d *Device) CheckPassphrase(keyslot int, passphrase []byte) (int, error) {
	if d.cd == nil {
		return 0, ErrClosed
	}
	d.log.msg[0] = 0

	cPassphrase, err := newSecret(passphrase)
	if err != nil {
		return 0, err
	}
	defer C.crypt_safe_free(cPassphrase)

	// without a device name the passphrase is only checked
	r := C.crypt_activate_by_passphrase(d.cd, nil, C.int(keyslot),
		(*C.char)(cPassphrase), C.size_t(len(passphrase)), 0)
	if r < 0 {
		return 0, d.fail("crypt_activate_by_passphrase", r)
	}
	return int(r), nil
}

// RemoveKeyslot destroys a keyslot. It asks for no passphrase, so the
// caller checks before that another keyslot opens with a passphrase it
// has. The last keyslot in use is refused with ErrLastKeyslot.
func (d *Device) RemoveKeyslot(keyslot int) error {
	if d.cd == nil {
		return ErrClosed
	}
	d.log.msg[0] = 0

	if C.crypt_keyslot_status(d.cd, C.int(keyslot)) == C.CRYPT_SLOT_ACTIVE_LAST {
		return ErrLastKeyslot
	}
	if r := C.crypt_keyslot_destroy(d.cd, C.int(keyslot)); r < 0 {
		return d.fail("crypt_keyslot_destroy", r)
	}
	return nil
}

// fail turns a negative return value into an error that wraps the errno
// and carries the message libcryptsetup logged for it.
func (d *Device) fail(call string, r C.int) error {
	errno := syscall.Errno(-r)
	msg := C.GoString(&d.log.msg[0])
	d.log.msg[0] = 0
	if msg == "" {
		return fmt.Errorf("%s: %w", call, errno)
	}
	return fmt.Errorf("%s: %w: %s", call, errno, msg)
}

// newSecret copies a passphrase into memory that libcryptsetup wipes when
// it is freed with crypt_safe_free.
func newSecret(passphrase []byte) (unsafe.Pointer, error) {
	if len(passphrase) == 0 {
		return nil, ErrEmptyPassphrase
	}
	p := C.crypt_safe_alloc(C.size_t(len(passphrase)))
	if p == nil {
		return nil, syscall.ENOMEM
	}
	C.memcpy(p, unsafe.Pointer(&passphrase[0]), C.size_t(len(passphrase)))
	return p, nil
}
