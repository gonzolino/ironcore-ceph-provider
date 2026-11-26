// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

// Package luksheader changes the LUKS header of an RBD image. It joins
// the header session of go-ceph and the cryptsetup package, which know
// nothing of each other.
package luksheader

import (
	"fmt"

	"github.com/ceph/go-ceph/rbd"
	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup"
)

// Change stages the LUKS header of the image, lets change edit it and
// writes it back. The image stays usable for a client that has it open,
// which waits during the write. When change returns an error, nothing
// is written.
//
// dev is valid inside change only. Only one Change at a time may run on
// an image.
//
// Once Change has taken the lock, img takes it only when asked to, so a
// later write on img fails. Use a handle that is closed afterwards.
func Change(img *rbd.Image, change func(dev *cryptsetup.Device) error) error {
	hdr, err := img.EncryptionLUKSHeaderOpen()
	if err != nil {
		return fmt.Errorf("open header session: %w", err)
	}
	defer func() { _ = hdr.Close() }()

	path, err := hdr.Path()
	if err != nil {
		return err
	}
	dev, err := cryptsetup.Open(path)
	if err != nil {
		return fmt.Errorf("load staged header: %w", err)
	}
	defer dev.Close()
	if err := change(dev); err != nil {
		return err
	}
	// libcryptsetup lets go of the file before librbd reads it
	dev.Close()

	// no other client gets the lock between two writes of the header
	if err := img.LockAcquire(rbd.LockModeExclusiveTransient); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	err = hdr.Commit()
	if lerr := img.LockRelease(); err == nil && lerr != nil {
		return fmt.Errorf("release lock: %w", lerr)
	}
	if err != nil {
		return fmt.Errorf("commit header: %w", err)
	}
	return nil
}
