// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

package luksheader_test

import (
	"errors"
	"syscall"
	"testing"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup"
	"github.com/ironcore-dev/ceph-provider/internal/luksheader"
)

// rotate runs luksheader.Rotate on an image handle of its own.
func rotate(t *testing.T, ioctx *rados.IOContext, name string, oldPassphrase, newPassphrase []byte) error {
	t.Helper()
	img, err := rbd.OpenImage(ioctx, name, rbd.NoSnapshot)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer func() { _ = img.Close() }()
	err = luksheader.Rotate(img, oldPassphrase, newPassphrase)
	notLockOwner(t, img)
	return err
}

// keyslots lists the keyslots of the image and writes nothing.
func keyslots(t *testing.T, ioctx *rados.IOContext, name string) []cryptsetup.Keyslot {
	t.Helper()
	errStop := errors.New("stop")
	var list []cryptsetup.Keyslot
	err := change(t, ioctx, name, func(dev *cryptsetup.Device) error {
		var err error
		if list, err = dev.Keyslots(); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("list keyslots: %v", err)
	}
	return list
}

// rotated checks that the image has one keyslot with the cost of a
// rotation, opens with newPassphrase and does not open with oldPassphrase.
func rotated(t *testing.T, ioctx *rados.IOContext, name string) {
	t.Helper()
	if got := keyslots(t, ioctx, name); len(got) != 1 || got[0].PBKDF != fixedCost {
		t.Errorf("keyslots: got %+v, want one with %+v", got, fixedCost)
	}
	if err := loads(t, ioctx, name, newPassphrase); err != nil {
		t.Errorf("new passphrase: %v", err)
	}
	if err := loads(t, ioctx, name, oldPassphrase); err == nil {
		t.Error("old passphrase: image still loads")
	}
}

// Needs the cluster of testPool.
func TestRotate(t *testing.T) {
	ioctx := testPool(t)

	t.Run("rotate", func(t *testing.T) {
		const name = "rotate"
		newImage(t, ioctx, name)
		if err := rotate(t, ioctx, name, oldPassphrase, newPassphrase); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		rotated(t, ioctx, name)

		// a second call finds nothing to do
		if err := rotate(t, ioctx, name, oldPassphrase, newPassphrase); err != nil {
			t.Fatalf("rotate again: %v", err)
		}
		rotated(t, ioctx, name)
	})

	// a call that follows one which stopped after the add
	t.Run("afterAdd", func(t *testing.T) {
		const name = "after-add"
		newImage(t, ioctx, name)
		err := change(t, ioctx, name, func(dev *cryptsetup.Device) error {
			_, err := addNew(dev)
			return err
		})
		if err != nil {
			t.Fatalf("add keyslot: %v", err)
		}
		if err := rotate(t, ioctx, name, oldPassphrase, newPassphrase); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		rotated(t, ioctx, name)
	})

	t.Run("wrongOldPassphrase", func(t *testing.T) {
		const name = "wrong-old"
		newImage(t, ioctx, name)
		err := rotate(t, ioctx, name, []byte("not-the-passphrase"), newPassphrase)
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("rotate: got %v, want %v", err, syscall.EPERM)
		}
		if got := keyslots(t, ioctx, name); len(got) != 1 {
			t.Errorf("keyslots: got %+v, want one", got)
		}
		if err := loads(t, ioctx, name, oldPassphrase); err != nil {
			t.Errorf("passphrase of the image: %v", err)
		}
	})

	t.Run("samePassphrase", func(t *testing.T) {
		const name = "same"
		newImage(t, ioctx, name)
		err := rotate(t, ioctx, name, oldPassphrase, oldPassphrase)
		if !errors.Is(err, luksheader.ErrSamePassphrase) {
			t.Fatalf("rotate: got %v, want %v", err, luksheader.ErrSamePassphrase)
		}
		if err := loads(t, ioctx, name, oldPassphrase); err != nil {
			t.Errorf("passphrase of the image: %v", err)
		}
	})
}
