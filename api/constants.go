// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package api

const (
	LabelsAnnotation      = "ceph-provider.ironcore.dev/labels"
	AnnotationsAnnotation = "ceph-provider.ironcore.dev/annotations"
	ClassLabel            = "ceph-provider.ironcore.dev/class"
	ManagerLabel          = "ceph-provider.ironcore.dev/manager"
	BucketManager         = "ceph-bucket-provider"
	VolumeManager         = "ceph-volume-provider"

	MachineArchitectureLabel = "common.ironcore.dev/architecture"
)

const (
	// VolumeImageIDPrefix is the prefix used for the image ID created for a volume.
	VolumeImageIDPrefix = "vol-"
	// OSImageBaseImagePrefix is the prefix used for the base image name created for an OS image.
	OSImageBaseImagePrefix = "base-"
	// OSImageSnapshotPrefix is the prefix used for the snapshot name created for an OS image.
	OSImageSnapshotPrefix = "snap-"
)
