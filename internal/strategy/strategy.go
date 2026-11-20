// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"crypto/rand"

	"github.com/ironcore-dev/ceph-provider/api"
	"github.com/ironcore-dev/ironcore/iri/common/idgen"
)

var VolumeStrategy = volumeStrategy{}

type volumeStrategy struct{}

func (volumeStrategy) PrepareForCreate(obj *api.Volume) {
	if obj.Status.State == "" {
		obj.Status = api.VolumeStatus{State: api.VolumeStatePending}
	}

	if obj.Spec.ImageName == "" {
		obj.Spec.ImageName = api.VolumeImageIDPrefix + obj.ID
	}
}

var ImageStrategy = imageStrategy{
	WWNGen: idgen.NewIDGen(rand.Reader, 16),
}

type imageStrategy struct {
	WWNGen idgen.IDGen
}

func (i imageStrategy) PrepareForCreate(obj *api.Image) {
	if obj.Status.State == "" {
		obj.Status = api.ImageStatus{State: api.ImageStatePending}
	}

	if obj.Spec.WWN == "" {
		obj.Spec.WWN = i.WWNGen.Generate()
	}
}

var SnapshotStrategy = snapshotStrategy{}

type snapshotStrategy struct{}

func (snapshotStrategy) PrepareForCreate(obj *api.Snapshot) {
	if obj.Status.State == "" {
		obj.Status = api.SnapshotStatus{State: api.SnapshotStatePending}
	}
}

var OSImageStrategy = osImageStrategy{}

type osImageStrategy struct{}

func (osImageStrategy) PrepareForCreate(obj *api.OSImage) {
	if obj.Status.State == "" {
		obj.Status = api.OSImageStatus{State: api.OSImageStatePending}
	}

	if obj.Spec.BaseImageName == "" {
		obj.Spec.BaseImageName = api.OSImageBaseImagePrefix + obj.ID
	}
	if obj.Spec.SnapshotName == "" {
		obj.Spec.SnapshotName = api.OSImageSnapshotPrefix + obj.ID[:7]
	}
}
