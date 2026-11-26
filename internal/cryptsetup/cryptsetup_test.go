// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build ceph_luks_header_session

package cryptsetup_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup"
	"github.com/ironcore-dev/ceph-provider/internal/cryptsetup/cryptsetuptest"
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

func newHeader(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "header")
	if err := cryptsetuptest.NewLUKS2File(path, oldPassphrase); err != nil {
		t.Fatalf("create LUKS2 file: %v", err)
	}
	return path
}

func open(t *testing.T, path string) *cryptsetup.Device {
	t.Helper()
	dev, err := cryptsetup.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(dev.Close)
	return dev
}

func keyslotIDs(t *testing.T, dev *cryptsetup.Device) []int {
	t.Helper()
	keyslots, err := dev.Keyslots()
	if err != nil {
		t.Fatalf("list keyslots: %v", err)
	}
	ids := make([]int, 0, len(keyslots))
	for _, keyslot := range keyslots {
		ids = append(ids, keyslot.ID)
	}
	return ids
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The steps of a rotation, on a file: add a keyslot, check both
// passphrases, remove the old keyslot, check again.
func TestAddCheckRemove(t *testing.T) {
	path := newHeader(t)
	dev := open(t, path)

	if got := dev.Type(); got != "LUKS2" {
		t.Errorf("type: got %q, want LUKS2", got)
	}
	if ids := keyslotIDs(t, dev); !equal(ids, []int{0}) {
		t.Fatalf("keyslots after format: got %v, want [0]", ids)
	}

	if err := dev.SetPBKDF(fixedCost); err != nil {
		t.Fatalf("set PBKDF: %v", err)
	}
	added, err := dev.AddKeyslot(cryptsetup.AnyKeyslot, oldPassphrase, newPassphrase)
	if err != nil {
		t.Fatalf("add keyslot: %v", err)
	}
	if added != 1 {
		t.Errorf("added keyslot: got %d, want 1", added)
	}

	keyslots, err := dev.Keyslots()
	if err != nil {
		t.Fatalf("list keyslots: %v", err)
	}
	if len(keyslots) != 2 {
		t.Fatalf("keyslots after add: got %+v, want two", keyslots)
	}
	if got := keyslots[0].PBKDF.Type; got != cryptsetup.PBKDF2 {
		t.Errorf("PBKDF of keyslot 0: got %q, want pbkdf2", got)
	}
	// the new keyslot has the cost that was set, not one measured here
	if got := keyslots[1].PBKDF; got != fixedCost {
		t.Errorf("PBKDF of keyslot 1: got %+v, want %+v", got, fixedCost)
	}

	if slot, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, newPassphrase); err != nil || slot != added {
		t.Errorf("new passphrase: keyslot %d, err %v, want keyslot %d", slot, err, added)
	}
	if slot, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, oldPassphrase); err != nil || slot != 0 {
		t.Errorf("old passphrase: keyslot %d, err %v, want keyslot 0", slot, err)
	}
	if _, err := dev.CheckPassphrase(added, oldPassphrase); !errors.Is(err, syscall.EPERM) {
		t.Errorf("old passphrase on the new keyslot: got %v, want EPERM", err)
	}

	if err := dev.RemoveKeyslot(0); err != nil {
		t.Fatalf("remove keyslot 0: %v", err)
	}
	if ids := keyslotIDs(t, dev); !equal(ids, []int{added}) {
		t.Errorf("keyslots after remove: got %v, want [%d]", ids, added)
	}
	if _, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, oldPassphrase); !errors.Is(err, syscall.EPERM) {
		t.Errorf("old passphrase after remove: got %v, want EPERM", err)
	}
	if err := dev.RemoveKeyslot(added); !errors.Is(err, cryptsetup.ErrLastKeyslot) {
		t.Errorf("remove the last keyslot: got %v, want ErrLastKeyslot", err)
	}

	// the changes are in the file, not only in the handle
	dev.Close()
	reopened := open(t, path)
	if ids := keyslotIDs(t, reopened); !equal(ids, []int{added}) {
		t.Errorf("keyslots after reopen: got %v, want [%d]", ids, added)
	}
	if slot, err := reopened.CheckPassphrase(cryptsetup.AnyKeyslot, newPassphrase); err != nil || slot != added {
		t.Errorf("new passphrase after reopen: keyslot %d, err %v", slot, err)
	}
}

func TestWrongPassphrase(t *testing.T) {
	dev := open(t, newHeader(t))

	_, err := dev.AddKeyslot(cryptsetup.AnyKeyslot, []byte("wrong"), newPassphrase)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("add with a wrong passphrase: got %v, want EPERM", err)
	}
	// the message of libcryptsetup is part of the error
	if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error without the message of libcryptsetup: %v", err)
	}
	if ids := keyslotIDs(t, dev); !equal(ids, []int{0}) {
		t.Errorf("keyslots after a failed add: got %v, want [0]", ids)
	}
}

func TestEmptyPassphrase(t *testing.T) {
	dev := open(t, newHeader(t))

	if _, err := dev.AddKeyslot(cryptsetup.AnyKeyslot, oldPassphrase, nil); !errors.Is(err, cryptsetup.ErrEmptyPassphrase) {
		t.Errorf("add an empty passphrase: got %v, want ErrEmptyPassphrase", err)
	}
	if _, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, nil); !errors.Is(err, cryptsetup.ErrEmptyPassphrase) {
		t.Errorf("check an empty passphrase: got %v, want ErrEmptyPassphrase", err)
	}
}

func TestOpenNoHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zeros")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cryptsetup.Open(path); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("open a file without a header: got %v, want EINVAL", err)
	}
	if _, err := cryptsetup.Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("open a missing file: got no error")
	}
}

func TestClosed(t *testing.T) {
	dev := open(t, newHeader(t))
	dev.Close()
	dev.Close()

	if got := dev.Type(); got != "" {
		t.Errorf("type: got %q, want empty", got)
	}
	if _, err := dev.Keyslots(); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("keyslots: got %v, want ErrClosed", err)
	}
	if err := dev.SetPBKDF(fixedCost); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("set PBKDF: got %v, want ErrClosed", err)
	}
	if _, err := dev.AddKeyslot(cryptsetup.AnyKeyslot, oldPassphrase, newPassphrase); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("add keyslot: got %v, want ErrClosed", err)
	}
	if _, err := dev.CheckPassphrase(cryptsetup.AnyKeyslot, oldPassphrase); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("check passphrase: got %v, want ErrClosed", err)
	}
	if err := dev.RemoveKeyslot(0); !errors.Is(err, cryptsetup.ErrClosed) {
		t.Errorf("remove keyslot: got %v, want ErrClosed", err)
	}
}
