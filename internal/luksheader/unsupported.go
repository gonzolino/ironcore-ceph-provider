// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !ceph_luks_header_session

// Package luksheader changes the LUKS header of an RBD image. This build
// has no LUKS header session in librbd, so every call returns
// ErrUnsupported.
package luksheader

import "github.com/ceph/go-ceph/rbd"

func AddPassphrase(_ *rbd.Image, _, _ []byte) error { return ErrUnsupported }

func RemovePassphrase(_ *rbd.Image, _, _ []byte) error { return ErrUnsupported }

func Rotate(_ *rbd.Image, _, _ []byte) error { return ErrUnsupported }
