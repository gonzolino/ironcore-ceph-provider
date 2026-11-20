// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/ceph-provider/api"
	"github.com/ironcore-dev/ironcore-image/oci/image"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	"k8s.io/client-go/util/workqueue"
)

type VolumeReconcilerOptions struct {
	WorkerSize int
}

func NewVolumeReconciler(
	log logr.Logger,
	registry image.Source,
	snapshotStore store.Store[*api.Snapshot],
	imageStore store.Store[*api.Image],
	osImageStore store.Store[*api.OSImage],
	volumeStore store.Store[*api.Volume],
	events event.Source[*api.Volume],
	imageEvents event.Source[*api.Image],
	osImageEvents event.Source[*api.OSImage],
	snapshotEvents event.Source[*api.Snapshot],
	opts VolumeReconcilerOptions,
) (*VolumeReconciler, error) {
	if registry == nil {
		return nil, fmt.Errorf("must specify registry")
	}

	if snapshotStore == nil {
		return nil, fmt.Errorf("must specify snapshot store")
	}

	if imageStore == nil {
		return nil, fmt.Errorf("must specify image store")
	}

	if osImageStore == nil {
		return nil, fmt.Errorf("must specify os image store")
	}

	if volumeStore == nil {
		return nil, fmt.Errorf("must specify volume store")
	}

	if events == nil {
		return nil, fmt.Errorf("must specify events")
	}

	if imageEvents == nil {
		return nil, fmt.Errorf("must specify image events")
	}

	if osImageEvents == nil {
		return nil, fmt.Errorf("must specify os image events")
	}

	if snapshotEvents == nil {
		return nil, fmt.Errorf("must specify snapshot events")
	}

	if opts.WorkerSize == 0 {
		opts.WorkerSize = 15
	}

	return &VolumeReconciler{
		log:            log,
		registry:       registry,
		queue:          workqueue.NewTypedRateLimitingQueue[string](workqueue.DefaultTypedControllerRateLimiter[string]()),
		snapshotStore:  snapshotStore,
		imageStore:     imageStore,
		osImageStore:   osImageStore,
		volumeStore:    volumeStore,
		events:         events,
		imageEvents:    imageEvents,
		osImageEvents:  osImageEvents,
		snapshotEvents: snapshotEvents,
		workerSize:     opts.WorkerSize,
	}, nil
}

type VolumeReconciler struct {
	log logr.Logger

	registry image.Source
	queue    workqueue.TypedRateLimitingInterface[string]

	snapshotStore store.Store[*api.Snapshot]
	imageStore    store.Store[*api.Image]
	osImageStore  store.Store[*api.OSImage]
	volumeStore   store.Store[*api.Volume]

	events         event.Source[*api.Volume]
	imageEvents    event.Source[*api.Image]
	osImageEvents  event.Source[*api.OSImage]
	snapshotEvents event.Source[*api.Snapshot]

	workerSize int
}

func (r *VolumeReconciler) Start(ctx context.Context) error {
	log := r.log

	reg, err := r.events.AddHandler(event.HandlerFunc[*api.Volume](func(event event.Event[*api.Volume]) {
		r.queue.Add(event.Object.ID)
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.events.RemoveHandler(reg)
	}()

	imageReg, err := r.imageEvents.AddHandler(event.HandlerFunc[*api.Image](func(evt event.Event[*api.Image]) {
		if evt.Type == event.TypeDeleted || (evt.Type == event.TypeUpdated && evt.Object.Status.State != api.ImageStatePending) {
			r.requeueVolumesForImage(ctx, evt.Object.ID)
		}
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.imageEvents.RemoveHandler(imageReg)
	}()

	osImgEventReg, err := r.osImageEvents.AddHandler(event.HandlerFunc[*api.OSImage](func(evt event.Event[*api.OSImage]) {
		if evt.Type == event.TypeDeleted || (evt.Type == event.TypeUpdated && evt.Object.Status.State != api.OSImageStatePending) {
			r.requeueVolumesForOSImage(ctx, evt.Object.ID)
		}
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.osImageEvents.RemoveHandler(osImgEventReg)
	}()

	snapshotReg, err := r.snapshotEvents.AddHandler(event.HandlerFunc[*api.Snapshot](func(evt event.Event[*api.Snapshot]) {
		if evt.Type == event.TypeDeleted || (evt.Type == event.TypeUpdated && evt.Object.Status.State != api.SnapshotStatePending) {
			r.requeueVolumesForSnapshot(ctx, evt.Object.ID)
		}
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.snapshotEvents.RemoveHandler(snapshotReg)
	}()

	go func() {
		<-ctx.Done()
		r.queue.ShutDown()
	}()

	var wg sync.WaitGroup
	for i := 0; i < r.workerSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r.processNextWorkItem(ctx, log) {
			}
		}()
	}

	wg.Wait()
	return nil
}

func (r *VolumeReconciler) requeueVolumesForImage(ctx context.Context, imageID string) {
	volumes, err := r.volumeStore.List(ctx, store.MatchingFields{api.VolumeSpecImageNameField: imageID})
	if err != nil {
		r.log.Error(err, "failed to list volumes for image event requeue")
		return
	}
	for _, vol := range volumes {
		r.queue.Add(vol.ID)
	}
}

func (r *VolumeReconciler) requeueVolumesForOSImage(ctx context.Context, osImageID string) {
	volumes, err := r.volumeStore.List(ctx, store.MatchingFields{api.VolumeSpecSourceOSImageField: osImageID})
	if err != nil {
		r.log.Error(err, "failed to list volumes for OS image event requeue")
		return
	}
	for _, vol := range volumes {
		r.queue.Add(vol.ID)
	}
}

func (r *VolumeReconciler) requeueVolumesForSnapshot(ctx context.Context, snapshotID string) {
	volumes, err := r.volumeStore.List(ctx, store.MatchingFields{api.VolumeSpecSourceSnapshotSourceField: snapshotID})
	if err != nil {
		r.log.Error(err, "failed to list volumes for snapshot event requeue")
		return
	}
	for _, vol := range volumes {
		r.queue.Add(vol.ID)
	}
}

func (r *VolumeReconciler) processNextWorkItem(ctx context.Context, log logr.Logger) bool {
	id, shutdown := r.queue.Get()
	if shutdown {
		return false
	}
	defer r.queue.Done(id)

	log = log.WithValues("volumeId", id)
	ctx = logr.NewContext(ctx, log)

	if err := r.reconcileVolume(ctx, id); err != nil {
		log.Error(err, "failed to reconcile volume")
		r.queue.AddRateLimited(id)
		return true
	}

	r.queue.Forget(id)
	return true
}

const (
	VolumeFinalizer = "volume"
)

func (r *VolumeReconciler) reconcileVolume(ctx context.Context, id string) error {
	log := logr.FromContextOrDiscard(ctx)

	log.V(1).Info("Get volume from store")
	volume, err := r.volumeStore.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to fetch volume from store: %w", err)
		}
		return nil
	}

	if volume.DeletedAt != nil {
		if err := r.deleteVolume(ctx, log, volume); err != nil {
			return fmt.Errorf("failed to delete volume: %w", err)
		}
		return nil
	}

	if _, added, err := addFinalizer(ctx, r.volumeStore, volume, VolumeFinalizer); err != nil {
		return fmt.Errorf("failed to set finalizers: %w", err)
	} else if added {
		log.V(1).Info("Added finalizer")
		return nil
	}

	imageID := volume.Spec.ImageName
	img, err := r.imageStore.Get(ctx, imageID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to get image: %w", err)
		}

		// Image does not exist, create it based on volume source
		volumeSource := volume.Spec.Source
		switch {
		case volumeSource.OSImage == nil && volumeSource.Snapshot == nil:
			return r.reconcileEmptyVolume(ctx, log, volume)
		case volumeSource.OSImage != nil && volumeSource.Snapshot == nil:
			return r.reconcileOSVolume(ctx, log, volume)
		case volumeSource.OSImage == nil && volumeSource.Snapshot != nil:
			return r.reconcileRestoredVolume(ctx, log, volume)
		default:
			return fmt.Errorf("invalid volume specification")
		}
	}

	// If the image was cloned from a snapshot that has since been deleted,
	// clear SnapshotSource so the image proceeds as standalone.
	if img.Spec.SnapshotSource != nil {
		snapshotID := *img.Spec.SnapshotSource
		if _, err := r.snapshotStore.Get(ctx, snapshotID); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("failed to get snapshot source: %w", err)
			}
			log.Info("Image snapshot source not found, removing it from the image spec", "imageId", img.ID, "snapshotId", snapshotID)
			img.Spec.SnapshotSource = nil
			if _, err := r.imageStore.Update(ctx, img); err != nil {
				return fmt.Errorf("failed to remove snapshot source on image: %w", err)
			}
			return nil
		}
	}

	// Image exists, reconcile its state before syncing the volume.
	switch img.Status.State {
	case api.ImageStatePending:
		log.Info("Image is pending, waiting", "imageId", img.ID)
		return nil
	case api.ImageStateAvailable:
		log.V(1).Info("Image is available", "imageId", img.ID)
	default:
		return fmt.Errorf("image %s in unexpected state: %s", img.ID, img.Status.State)
	}

	// Reconcile mutable image spec fields (e.g. size) from the volume spec.
	if changed, err := r.syncImageSpec(ctx, volume, img); err != nil {
		return err
	} else if changed {
		log.Info("Updated image spec from volume spec", "imageId", img.ID)
		return nil
	}

	// Reconcile volume status from the settled image status.
	var desiredAccess *api.VolumeAccess
	if img.Status.Access != nil {
		desiredAccess = &api.VolumeAccess{
			Monitors: img.Status.Access.Monitors,
			Handle:   img.Status.Access.Handle,
			WWN:      img.Spec.WWN,
			User:     img.Status.Access.User,
			UserKey:  img.Status.Access.UserKey,
		}
	}
	desiredEncryption := api.VolumeEncryptionState("")
	if img.Status.Encryption == api.EncryptionStateHeaderSet {
		desiredEncryption = api.VolumeEncryptionStateHeaderSet
	}

	needsUpdate := volume.Status.State != api.VolumeStateAvailable ||
		volume.Status.Size != img.Status.Size ||
		volume.Status.VolumeEncryption != desiredEncryption ||
		!volumeAccessEqual(volume.Status.Access, desiredAccess)

	if !needsUpdate {
		log.V(1).Info("Volume is up to date, nothing to do")
		return nil
	}

	log.Info("Image is available, updating volume status", "imageId", img.ID)
	volume.Status.State = api.VolumeStateAvailable
	volume.Status.Size = img.Status.Size
	volume.Status.VolumeEncryption = desiredEncryption
	volume.Status.Access = desiredAccess
	if _, err := r.volumeStore.Update(ctx, volume); err != nil {
		return fmt.Errorf("failed to update volume status: %w", err)
	}
	return nil
}

func (r *VolumeReconciler) setVolumeImageRef(ctx context.Context, volume *api.Volume, imageID string) error {
	if volume.Status.ImageRef == imageID {
		return nil
	}
	volume.Status.ImageRef = imageID
	if _, err := r.volumeStore.Update(ctx, volume); err != nil {
		return fmt.Errorf("failed to update volume with image ref: %w", err)
	}
	return nil
}

// syncImageSpec updates the image spec to match the volume's desired state for
// the fields that can change after creation. It returns true if the image was updated.
func (r *VolumeReconciler) syncImageSpec(ctx context.Context, volume *api.Volume, img *api.Image) (bool, error) {
	changed := false
	if img.Spec.Size != volume.Spec.Size {
		img.Spec.Size = volume.Spec.Size
		changed = true
	}

	if !changed {
		return false, nil
	}
	if _, err := r.imageStore.Update(ctx, img); err != nil {
		return false, fmt.Errorf("failed to update image spec: %w", err)
	}
	return true, nil
}

func buildVolumeImage(volume *api.Volume, snapshotSource *string) *api.Image {
	return &api.Image{
		Metadata: apiutils.Metadata{
			ID: volume.Spec.ImageName,
		},
		Spec: api.ImageSpec{
			Size:   volume.Spec.Size,
			Limits: volume.Spec.Limits,
			Encryption: api.EncryptionSpec{
				Type:                api.EncryptionType(volume.Spec.VolumeEncryption.Type),
				EncryptedPassphrase: volume.Spec.VolumeEncryption.EncryptedPassphrase,
			},
			SnapshotSource: snapshotSource,
		},
	}
}

func (r *VolumeReconciler) reconcileEmptyVolume(ctx context.Context, log logr.Logger, volume *api.Volume) error {
	log.V(1).Info("Reconciling empty volume")

	img := buildVolumeImage(volume, nil)

	createdImage, err := createOrGet(ctx, log, r.imageStore, img, "Image created", "Image already exists")
	if err != nil {
		return fmt.Errorf("failed to create or get image: %w", err)
	}

	return r.setVolumeImageRef(ctx, volume, createdImage.ID)
}

func (r *VolumeReconciler) reconcileOSVolume(ctx context.Context, log logr.Logger, volume *api.Volume) error {
	log.V(1).Info("Reconciling OS volume")

	if volume.Spec.Source.OSImage == nil {
		return fmt.Errorf("OS image is nil")
	}
	osImageRef := *volume.Spec.Source.OSImage
	osImage, err := r.osImageStore.Get(ctx, osImageRef)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to get OS image %s: %w", osImageRef, err)
		}
	}

	switch osImage.Status.State {
	case api.OSImageStatePending:
		log.Info("OS image is pending, waiting", "osImageId", osImage.ID)
		return nil
	case api.OSImageStateAvailable:
		log.V(1).Info("OS image is available", "osImageId", osImage.ID)
	default:
		return fmt.Errorf("OS image %s in unexpected state: %s", osImage.ID, osImage.Status.State)
	}

	snapshotRef := osImage.Status.SnapshotRef
	if snapshotRef == nil {
		return fmt.Errorf("OS image %s has no snapshot reference", osImage.ID)
	}
	volumeImage := buildVolumeImage(volume, snapshotRef)
	log.V(1).Info("Creating volume image from OS image snapshot", "snapshotId", *snapshotRef, "imageId", volumeImage.ID)

	createdImage, err := createOrGet(ctx, log, r.imageStore, volumeImage, "Volume image created", "Volume image already exists")
	if err != nil {
		return fmt.Errorf("failed to create or get volume image: %w", err)
	}

	return r.setVolumeImageRef(ctx, volume, createdImage.ID)
}

func (r *VolumeReconciler) reconcileRestoredVolume(ctx context.Context, log logr.Logger, volume *api.Volume) error {
	log.V(1).Info("Reconciling volume restored from snapshot")

	if volume.Spec.Source.Snapshot == nil {
		return fmt.Errorf("source snapshot is nil")
	}
	snapshotID := *volume.Spec.Source.Snapshot

	snapshot, err := r.snapshotStore.Get(ctx, snapshotID)
	if err != nil {
		return fmt.Errorf("failed to get snapshot %s: %w", snapshotID, err)
	}

	switch snapshot.Status.State {
	case api.SnapshotStatePending:
		log.Info("Snapshot is pending, waiting", "snapshotId", snapshotID)
		return nil
	case api.SnapshotStateReady:
		log.V(1).Info("Snapshot is ready", "snapshotId", snapshotID)
	case api.SnapshotStateFailed:
		return fmt.Errorf("snapshot %s failed", snapshotID)
	default:
		return fmt.Errorf("snapshot %s in unexpected state: %s", snapshotID, snapshot.Status.State)
	}

	// Create new Image from snapshot
	img := buildVolumeImage(volume, &snapshotID)
	imageID := img.ID
	log.V(1).Info("Creating image from snapshot", "snapshotId", snapshotID, "imageId", imageID)

	createdImage, err := createOrGet(ctx, log, r.imageStore, img, "Image created from snapshot", "Image already exists")
	if err != nil {
		return fmt.Errorf("failed to create or get image: %w", err)
	}

	// Update volume status with ImageRef
	return r.setVolumeImageRef(ctx, volume, createdImage.ID)
}

func (r *VolumeReconciler) deleteVolume(ctx context.Context, log logr.Logger, volume *api.Volume) error {
	if !slices.Contains(volume.Finalizers, VolumeFinalizer) {
		log.V(1).Info("volume has no finalizer: done")
		return nil
	}

	// Delete the volume's Image if it exists
	imageRef := volume.Status.ImageRef
	if imageRef != "" {
		if err := r.imageStore.Delete(ctx, imageRef); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("failed to delete image %s: %w", imageRef, err)
			}
			log.V(1).Info("Image already deleted", "imageId", imageRef)
		} else {
			log.Info("Image deleted", "imageId", imageRef)
		}
	}

	// Note: We do NOT delete base images or snapshots for OS volumes
	// as they may be shared by multiple volumes (golden image pattern).
	// Clean up of unused base images and snapshots can be added later.

	if _, removed, err := removeFinalizer(ctx, r.volumeStore, volume, VolumeFinalizer); store.IgnoreErrNotFound(err) != nil {
		return fmt.Errorf("failed to update volume metadata: %w", err)
	} else if removed {
		log.V(1).Info("Removed finalizer")
	}
	return nil
}

func volumeAccessEqual(a, b *api.VolumeAccess) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Monitors == b.Monitors && a.Handle == b.Handle && a.User == b.User && a.UserKey == b.UserKey
}
