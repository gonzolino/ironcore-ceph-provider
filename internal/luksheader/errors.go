// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package luksheader

import "errors"

// ErrSamePassphrase is returned when the two passphrases of a call are
// equal.
var ErrSamePassphrase = errors.New("luksheader: both passphrases are the same")

// ErrUnsupported is returned by a build without the tag
// ceph_luks_header_session, whose librbd has no LUKS header session.
var ErrUnsupported = errors.New("luksheader: built without the LUKS header session of librbd")
