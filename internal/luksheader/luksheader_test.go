// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

package luksheader_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup"
	"github.com/ironcore-dev/ceph-provider/internal/luksheader"
)

var (
	oldPassphrase = []byte("old-passphrase")
	newPassphrase = []byte("new-passphrase")

	// the cost a rotation gives a new keyslot
	fixedCost = cryptsetup.PBKDF{
		Type:       cryptsetup.Argon2id,
		Iterations: 4,
		MemoryKiB:  64 * 1024,
		Threads:    4,
	}
)

func luks2(passphrase []byte) rbd.EncryptionOptionsLUKS2 {
	return rbd.EncryptionOptionsLUKS2{
		Alg:        rbd.EncryptionAlgorithmAES256,
		Passphrase: passphrase,
	}
}

// loads reports whether librbd opens the encryption of the image with
// the passphrase.
func loads(t *testing.T, ioctx *rados.IOContext, name string, passphrase []byte) error {
	t.Helper()
	img, err := rbd.OpenImage(ioctx, name, rbd.NoSnapshot)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer func() { _ = img.Close() }()
	return img.EncryptionLoad(luks2(passphrase))
}

// change runs luksheader.Change on an image handle of its own.
func change(t *testing.T, ioctx *rados.IOContext, name string, fn func(dev *cryptsetup.Device) error) error {
	t.Helper()
	img, err := rbd.OpenImage(ioctx, name, rbd.NoSnapshot)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer func() { _ = img.Close() }()
	err = luksheader.Change(img, fn)
	notLockOwner(t, img)
	return err
}

// notLockOwner checks that the handle gave the exclusive lock back.
// Without the release a handle keeps the lock that the write of the header
// took.
func notLockOwner(t *testing.T, img *rbd.Image) {
	t.Helper()
	if owner, err := img.LockIsExclusiveOwner(); err != nil || owner {
		t.Errorf("exclusive lock after the change: owner %v, err %v", owner, err)
	}
}

// testPool returns a pool of its own on the cluster that CEPH_CONFIG_FILE
// names, whose librbd has the header session. Without the variable the
// test is skipped.
func testPool(t *testing.T) *rados.IOContext {
	t.Helper()
	conf := os.Getenv("CEPH_CONFIG_FILE")
	if conf == "" {
		t.Skip("CEPH_CONFIG_FILE is not set")
	}

	conn, err := rados.NewConn()
	if err != nil {
		t.Fatalf("new connection: %v", err)
	}
	if err := conn.ReadConfigFile(conf); err != nil {
		t.Fatalf("read %s: %v", conf, err)
	}
	if err := conn.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Shutdown)

	pool := fmt.Sprintf("luksheader-test-%d", time.Now().UnixNano())
	if err := conn.MakePool(pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { _ = conn.DeletePool(pool) })
	ioctx, err := conn.OpenIOContext(pool)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(ioctx.Destroy)
	return ioctx
}

// newImage creates an image that is formatted with oldPassphrase.
func newImage(t *testing.T, ioctx *rados.IOContext, name string) {
	t.Helper()
	if err := rbd.CreateImage(ioctx, name, 64<<20, rbd.NewRbdImageOptions()); err != nil {
		t.Fatalf("create image: %v", err)
	}
	img, err := rbd.OpenImage(ioctx, name, rbd.NoSnapshot)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	err = img.EncryptionFormat(luks2(oldPassphrase))
	_ = img.Close()
	if err != nil {
		t.Fatalf("format image: %v", err)
	}
}

func addNew(dev *cryptsetup.Device) (int, error) {
	if err := dev.SetPBKDF(fixedCost); err != nil {
		return -1, fmt.Errorf("set PBKDF: %w", err)
	}
	return dev.AddKeyslot(cryptsetup.AnyKeyslot, oldPassphrase, newPassphrase)
}

// A passphrase is replaced on an RBD image in two changes: the new
// keyslot is added, then it is checked and the old one is removed. Needs
// the cluster of testPool.
func TestChange(t *testing.T) {
	ioctx := testPool(t)
	const name = "volume"
	newImage(t, ioctx, name)

	// a change that fails after it edited the staged header writes nothing
	errStop := errors.New("stop")
	var kept *cryptsetup.Device
	err := change(t, ioctx, name, func(dev *cryptsetup.Device) error {
		kept = dev
		if _, err := addNew(dev); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("failed change: got %v, want %v", err, errStop)
	}
	if err := loads(t, ioctx, name, newPassphrase); err == nil {
		t.Error("new passphrase after a failed change: image loads")
	}
	// the handle ends with the change
	if _, err := kept.Keyslots(); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("handle after the change: got %v, want %v", err, cryptsetup.ErrClosed)
	}

	added := -1
	err = change(t, ioctx, name, func(dev *cryptsetup.Device) error {
		var err error
		added, err = addNew(dev)
		return err
	})
	if err != nil {
		t.Fatalf("add keyslot: %v", err)
	}
	if err := loads(t, ioctx, name, newPassphrase); err != nil {
		t.Fatalf("new passphrase after add: %v", err)
	}
	if err := loads(t, ioctx, name, oldPassphrase); err != nil {
		t.Errorf("old passphrase after add: %v", err)
	}

	err = change(t, ioctx, name, func(dev *cryptsetup.Device) error {
		// the header that came back from the image has the new keyslot
		keyslots, err := dev.Keyslots()
		if err != nil {
			return fmt.Errorf("list keyslots: %w", err)
		}
		if len(keyslots) != 2 || keyslots[1].ID != added || keyslots[1].PBKDF != fixedCost {
			t.Errorf("keyslots after add: got %+v, want keyslot %d with %+v", keyslots, added, fixedCost)
		}
		if slot, err := dev.CheckPassphrase(added, newPassphrase); err != nil || slot != added {
			return fmt.Errorf("new passphrase on keyslot %d: keyslot %d, err %v", added, slot, err)
		}
		old, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, oldPassphrase)
		if err != nil {
			return fmt.Errorf("find the old keyslot: %w", err)
		}
		return dev.RemoveKeyslot(old)
	})
	if err != nil {
		t.Fatalf("remove keyslot: %v", err)
	}
	if err := loads(t, ioctx, name, newPassphrase); err != nil {
		t.Errorf("new passphrase after remove: %v", err)
	}
	if err := loads(t, ioctx, name, oldPassphrase); err == nil {
		t.Error("old passphrase after remove: image still loads")
	}
}
