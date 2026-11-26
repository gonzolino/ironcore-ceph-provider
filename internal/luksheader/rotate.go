// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

package luksheader

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"

	"github.com/ceph/go-ceph/rbd"
	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup"
)

// keyslotCost is the key derivation of the keyslot that AddPassphrase
// adds. It is fixed, so the memory a rotation needs for it is the same on
// every host.
var keyslotCost = cryptsetup.PBKDF{
	Type:       cryptsetup.Argon2id,
	Iterations: 4,
	MemoryKiB:  64 * 1024,
	Threads:    4,
}

// AddPassphrase adds a keyslot for newPassphrase to the header of the
// image. passphrase has to open a keyslot of the image. Afterwards both
// open the image.
//
// When newPassphrase opens a keyslot already, nothing is added, so the
// call can be repeated after an error. The note on img at Change holds
// here too.
func AddPassphrase(img *rbd.Image, passphrase, newPassphrase []byte) error {
	if bytes.Equal(passphrase, newPassphrase) {
		return ErrSamePassphrase
	}
	return Change(img, func(dev *cryptsetup.Device) error {
		_, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, newPassphrase)
		if err == nil {
			// a call before this one added the keyslot
			return nil
		}
		if !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("check new passphrase: %w", err)
		}
		if err := dev.SetPBKDF(keyslotCost); err != nil {
			return fmt.Errorf("set key derivation: %w", err)
		}
		_, err = dev.AddKeyslot(cryptsetup.AnyKeyslot, passphrase, newPassphrase)
		return err
	})
}

// RemovePassphrase removes the keyslot of oldPassphrase from the header
// of the image. Before that it checks, on the header as the image has it
// now, that keepPassphrase opens a keyslot, so the image is never left
// without a passphrase the caller knows.
//
// When oldPassphrase opens no keyslot, nothing is removed, so the call
// can be repeated after an error. The note on img at Change holds here
// too.
func RemovePassphrase(img *rbd.Image, keepPassphrase, oldPassphrase []byte) error {
	if bytes.Equal(keepPassphrase, oldPassphrase) {
		return ErrSamePassphrase
	}
	return Change(img, func(dev *cryptsetup.Device) error {
		if _, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, keepPassphrase); err != nil {
			return fmt.Errorf("check passphrase to keep: %w", err)
		}
		old, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, oldPassphrase)
		if errors.Is(err, syscall.EPERM) {
			// a call before this one removed the keyslot
			return nil
		}
		if err != nil {
			return fmt.Errorf("find keyslot of old passphrase: %w", err)
		}
		return dev.RemoveKeyslot(old)
	})
}

// Rotate replaces the passphrase of the image in two changes of its
// header, AddPassphrase and RemovePassphrase. Between the two both
// passphrases open the image.
//
// After an error Rotate can be called again with the same passphrases. It
// skips what the call before it has done.
func Rotate(img *rbd.Image, oldPassphrase, newPassphrase []byte) error {
	if err := AddPassphrase(img, oldPassphrase, newPassphrase); err != nil {
		if errors.Is(err, ErrSamePassphrase) {
			return err
		}
		return fmt.Errorf("add keyslot for new passphrase: %w", err)
	}
	if err := RemovePassphrase(img, newPassphrase, oldPassphrase); err != nil {
		return fmt.Errorf("remove keyslot of old passphrase: %w", err)
	}
	return nil
}
