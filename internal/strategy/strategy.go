// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"crypto/rand"
	"fmt"

	"github.com/ironcore-dev/ceph-provider/api"
	"github.com/ironcore-dev/controller-utils/metautils"
	"github.com/ironcore-dev/ironcore/iri/common/idgen"
)

var VolumeStrategy = volumeStrategy{}

type volumeStrategy struct{}

func (volumeStrategy) PrepareForCreate(obj *api.Volume) {
	obj.Status = api.VolumeStatus{State: api.VolumeStatePending}
}

var ImageStrategy = imageStrategy{
	WWNGen: idgen.NewIDGen(rand.Reader, 16),
}

type imageStrategy struct {
	WWNGen idgen.IDGen
}

func (i imageStrategy) PrepareForCreate(obj *api.Image) {
	obj.Spec.WWN = i.WWNGen.Generate()
	obj.Status = api.ImageStatus{State: api.ImageStatePending}
}

var SnapshotStrategy = snapshotStrategy{}

type snapshotStrategy struct{}

func (snapshotStrategy) PrepareForCreate(obj *api.Snapshot) {
	obj.Status = api.SnapshotStatus{State: api.SnapshotStatePending}
}

var OSImageStrategy = osImageStrategy{}

type osImageStrategy struct{}

func (osImageStrategy) PrepareForCreate(obj *api.OSImage) {
	obj.Status = api.OSImageStatus{State: api.OSImageStatePending}

	// Preconfigure the OSImage with labels for base image and snapshot references.
	// This allows for efficient filtering and retrieval of OSImage objects based on their associated base image and snapshot.
	metautils.SetLabels(obj, map[string]string{
		api.OSImageBaseImageLabel: fmt.Sprintf("base-%s", obj.ID),
		api.OSImageSnapshotLabel:  fmt.Sprintf("snap-%s", obj.ID[:7]),
	})
}
