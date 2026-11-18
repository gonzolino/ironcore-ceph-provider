// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package volumeserver

import (
	"context"
	"fmt"

	"errors"

	"github.com/go-logr/logr"
	api2 "github.com/ironcore-dev/ceph-provider/api"
	"github.com/ironcore-dev/ceph-provider/internal/utils"
	iriv1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
)

func (s *Server) createVolumeSnapshot(ctx context.Context, log logr.Logger, volumeSnapshot *iriv1alpha1.VolumeSnapshot) (*api2.Snapshot, error) {
	log.V(2).Info("Check if volume snapshot's source volume exists")
	volumeID := volumeSnapshot.Spec.VolumeId
	volume, err := s.volumeStore.Get(ctx, volumeID)
	if err != nil {
		if errors.Is(err, utils.ErrVolumeNotFound) {
			return nil, fmt.Errorf("failed to get source volume %s: %w", volumeID, utils.ErrVolumeNotFound)
		}
		return nil, fmt.Errorf("failed to get source volume %s: %w", volumeID, err)
	}
	if volume.Status.State != api2.VolumeStateAvailable {
		return nil, fmt.Errorf("source volume %s is not available, current state is: %s", volumeID, volume.Status.State)
	}

	if volume.Status.ImageRef == "" {
		return nil, fmt.Errorf("source volume %s has no image ref", volumeID)
	}

	snapshot := &api2.Snapshot{
		Metadata: apiutils.Metadata{
			ID: s.idGen.Generate(),
		},
		Spec: api2.SnapshotSpec{
			ImageRef:   volume.Status.ImageRef,
			Protection: api2.SnapshotProtectionProtected,
		},
	}

	log.V(2).Info("Setting volume snapshot metadata")
	if err := api2.SetObjectMetadataFromMetadata(snapshot, volumeSnapshot.Metadata); err != nil {
		return nil, fmt.Errorf("failed to set volume snapshot metadata: %w", err)
	}
	api2.SetManagerLabel(snapshot, api2.VolumeManager)

	log.V(2).Info("Creating volume snapshot in store")
	snapshot, err = s.snapshotStore.Create(ctx, snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume snapshot: %w", err)
	}

	return snapshot, nil
}

func (s *Server) CreateVolumeSnapshot(ctx context.Context, req *iriv1alpha1.CreateVolumeSnapshotRequest) (*iriv1alpha1.CreateVolumeSnapshotResponse, error) {
	log := s.loggerFrom(ctx)
	log.V(1).Info("Creating volume snapshot")

	snapshot, err := s.createVolumeSnapshot(ctx, log, req.VolumeSnapshot)
	if err != nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("unable to create ceph snapshot: %w", err))
	}

	log = log.WithValues("VolumeSnapshotID", snapshot.ID, "State", snapshot.Status.State)

	log.V(1).Info("Converting volume snapshot to IRI volume snapshot")
	iriVolumeSnapshot, err := s.convertSnapshotToIRI(ctx, snapshot)
	if err != nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("unable to convert volume snapshot: %w", err))
	}

	log.V(1).Info("Volume snapshot created successfully")
	return &iriv1alpha1.CreateVolumeSnapshotResponse{
		VolumeSnapshot: iriVolumeSnapshot,
	}, nil
}
