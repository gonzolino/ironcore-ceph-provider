// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
)

const (
	OSImageAnnotation     = "os-image-id"
	OSImageBaseImageLabel = "base-image-id"
	OSImageSnapshotLabel  = "snapshot-id"
)

type OSImage struct {
	apiutils.Metadata `json:"metadata"`

	Spec   OSImageSpec   `json:"spec"`
	Status OSImageStatus `json:"status"`
}

type OSImageState string

const (
	OSImageStatePending   OSImageState = "Pending"
	OSImageStateAvailable OSImageState = "Available"
)

type OSImageSpec struct {
	Source OSImageSource `json:"source"`
}

type OSImageSource struct {
	Reference string `json:"reference"`
}

type OSImageStatus struct {
	State        OSImageState `json:"state"`
	Size         uint64       `json:"size"`
	BaseImageRef *string      `json:"baseImageRef"`
	SnapshotRef  *string      `json:"snapshotRef"`
}
