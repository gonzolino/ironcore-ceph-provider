// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package volumeserver

import (
	"context"
	"fmt"

	api2 "github.com/ironcore-dev/ceph-provider/api"
	iri "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
)

func (s *Server) convertSnapshotToIRI(ctx context.Context, snapshot *api2.Snapshot) (*iri.VolumeSnapshot, error) {
	metadata, err := api2.GetObjectMetadataFromObjectID(snapshot.Metadata)
	if err != nil {
		return nil, fmt.Errorf("error getting iri metadata: %w", err)
	}

	state, err := s.getIriSnapshotState(snapshot.Status.State)
	if err != nil {
		return nil, fmt.Errorf("error getting iri state: %w", err)
	}

	imageRef := snapshot.Spec.ImageRef
	volumes, err := s.volumeStore.List(ctx, store.MatchingFields{api2.VolumeSpecImageNameField: imageRef})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes with imageRef %s: %w", imageRef, err)
	}
	if len(volumes) != 1 {
		return nil, fmt.Errorf("expected exactly one volume with imageRef %s, but found %d", imageRef, len(volumes))
	}
	volumeId := volumes[0].ID

	return &iri.VolumeSnapshot{
		Metadata: metadata,
		Spec: &iri.VolumeSnapshotSpec{
			VolumeId: volumeId,
		},
		Status: &iri.VolumeSnapshotStatus{
			State: state,
			Size:  int64(snapshot.Status.Size),
		},
	}, nil
}

func (s *Server) getIriSnapshotState(state api2.SnapshotState) (iri.VolumeSnapshotState, error) {
	switch state {
	case api2.SnapshotStateReady:
		return iri.VolumeSnapshotState_VOLUME_SNAPSHOT_READY, nil
	case api2.SnapshotStatePending:
		return iri.VolumeSnapshotState_VOLUME_SNAPSHOT_PENDING, nil
	case api2.SnapshotStateFailed:
		return iri.VolumeSnapshotState_VOLUME_SNAPSHOT_FAILED, nil
	default:
		return 0, fmt.Errorf("unknown volume snapshot state '%q'", state)
	}
}
