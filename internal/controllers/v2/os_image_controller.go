// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ceph/go-ceph/rados"
	librbd "github.com/ceph/go-ceph/rbd"
	"github.com/go-logr/logr"
	providerapi "github.com/ironcore-dev/ceph-provider/api/v2"
	"github.com/ironcore-dev/ceph-provider/internal/rater"
	ironcoreimage "github.com/ironcore-dev/ironcore-image"
	"github.com/ironcore-dev/ironcore-image/oci/image"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/storeutils/store"
	"k8s.io/client-go/util/workqueue"
)

type OSImageReconcilerOptions struct {
	Monitors            string
	Client              string
	Pool                string
	PopulatorBufferSize int64
	WorkerSize          int
}

func NewOSImageReconciler(
	log logr.Logger,
	conn *rados.Conn,
	osImageStore store.Store[*providerapi.OSImage],
	imageStore store.Store[*providerapi.Image],
	snapshotStore store.Store[*providerapi.Snapshot],
	osImageEvents event.Source[*providerapi.OSImage],
	imageEvents event.Source[*providerapi.Image],
	snapshotEvents event.Source[*providerapi.Snapshot],
	opts OSImageReconcilerOptions,
) (*OSImageReconciler, error) {
	if conn == nil {
		return nil, fmt.Errorf("must specify conn")
	}

	if osImageStore == nil {
		return nil, fmt.Errorf("must specify os image store")
	}

	if imageStore == nil {
		return nil, fmt.Errorf("must specify image store")
	}

	if snapshotStore == nil {
		return nil, fmt.Errorf("must specify snapshot store")
	}

	if osImageEvents == nil {
		return nil, fmt.Errorf("must specify os image events")
	}

	if imageEvents == nil {
		return nil, fmt.Errorf("must specify image events")
	}

	if snapshotEvents == nil {
		return nil, fmt.Errorf("must specify snapshot events")
	}

	if opts.Pool == "" {
		return nil, fmt.Errorf("must specify pool")
	}

	if opts.Monitors == "" {
		return nil, fmt.Errorf("must specify monitors")
	}

	if opts.Client == "" {
		return nil, fmt.Errorf("must specify ceph client")
	}

	if opts.PopulatorBufferSize == 0 {
		opts.PopulatorBufferSize = 5 * 1024 * 1024
	}

	if opts.WorkerSize == 0 {
		opts.WorkerSize = 15
	}

	return &OSImageReconciler{
		log:                 log,
		conn:                conn,
		queue:               workqueue.NewTypedRateLimitingQueue[string](workqueue.DefaultTypedControllerRateLimiter[string]()),
		osImageStore:        osImageStore,
		imageStore:          imageStore,
		snapshotStore:       snapshotStore,
		osImageEvents:       osImageEvents,
		imageEvents:         imageEvents,
		snapshotEvents:      snapshotEvents,
		monitors:            opts.Monitors,
		client:              opts.Client,
		pool:                opts.Pool,
		populatorBufferSize: opts.PopulatorBufferSize,
		workerSize:          opts.WorkerSize,
	}, nil
}

type OSImageReconciler struct {
	log  logr.Logger
	conn *rados.Conn

	queue workqueue.TypedRateLimitingInterface[string]

	osImageStore  store.Store[*providerapi.OSImage]
	imageStore    store.Store[*providerapi.Image]
	snapshotStore store.Store[*providerapi.Snapshot]

	osImageEvents  event.Source[*providerapi.OSImage]
	imageEvents    event.Source[*providerapi.Image]
	snapshotEvents event.Source[*providerapi.Snapshot]

	monitors string
	client   string
	pool     string

	populatorBufferSize int64

	workerSize int
}

func (r *OSImageReconciler) Start(ctx context.Context) error {
	log := r.log

	osImgEventReg, err := r.osImageEvents.AddHandler(event.HandlerFunc[*providerapi.OSImage](func(evt event.Event[*providerapi.OSImage]) {
		r.queue.Add(evt.Object.ID)
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.osImageEvents.RemoveHandler(osImgEventReg)
	}()

	imgEventReg, err := r.imageEvents.AddHandler(event.HandlerFunc[*providerapi.Image](func(evt event.Event[*providerapi.Image]) {
		if evt.Type == event.TypeDeleted || (evt.Type == event.TypeUpdated && evt.Object.Status.State != providerapi.ImageStatePending) {
			r.requeueOSImagesForImage(ctx, evt.Object.ID)
		}
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.imageEvents.RemoveHandler(imgEventReg)
	}()

	snapEventReg, err := r.snapshotEvents.AddHandler(event.HandlerFunc[*providerapi.Snapshot](func(evt event.Event[*providerapi.Snapshot]) {
		if evt.Type == event.TypeDeleted || (evt.Type == event.TypeUpdated && evt.Object.Status.State != providerapi.SnapshotStatePending) {
			r.requeueOSImagesForSnapshot(ctx, evt.Object.ID)
		}
	}))
	if err != nil {
		return err
	}
	defer func() {
		_ = r.snapshotEvents.RemoveHandler(snapEventReg)
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

func (r *OSImageReconciler) requeueOSImagesForImage(ctx context.Context, imageID string) {
	osImages, err := r.osImageStore.List(ctx, store.MatchingLabels{"digest": imageID})
	if err != nil {
		r.log.Error(err, "failed to list OS images for base image event requeue")
		return
	}
	for _, osImage := range osImages {
		r.queue.Add(osImage.ID)
	}
}

func (r *OSImageReconciler) requeueOSImagesForSnapshot(ctx context.Context, snapshotID string) {
	osImages, err := r.osImageStore.List(ctx, store.MatchingLabels{"digest": snapshotID})
	if err != nil {
		r.log.Error(err, "failed to list OS images for snapshot event requeue")
		return
	}
	for _, osImage := range osImages {
		r.queue.Add(osImage.ID)
	}
}

func (r *OSImageReconciler) processNextWorkItem(ctx context.Context, log logr.Logger) bool {
	id, shutdown := r.queue.Get()
	if shutdown {
		return false
	}
	defer r.queue.Done(id)

	log = log.WithValues("osImageId", id)
	ctx = logr.NewContext(ctx, log)

	if err := r.reconcileOSImage(ctx, id); err != nil {
		log.Error(err, "failed to reconcile OS image")
		r.queue.AddRateLimited(id)
		return true
	}

	r.queue.Forget(id)
	return true
}

const (
	osImageFinalizer = "os-image"
)

func (r *OSImageReconciler) reconcileOSImage(ctx context.Context, id string) error {
	log := logr.FromContextOrDiscard(ctx)

	log.V(1).Info("Get OS image from store")
	osImage, err := r.osImageStore.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to fetch OS image from store: %w", err)
		}
		return nil
	}

	if osImage.DeletedAt != nil {
		if err := r.deleteOSImage(ctx, log, osImage); err != nil {
			return fmt.Errorf("failed to delete OS image: %w", err)
		}
		return nil
	}

	if _, added, err := addFinalizer(ctx, r.osImageStore, osImage, osImageFinalizer); err != nil {
		return fmt.Errorf("failed to set finalizers: %w", err)
	} else if added {
		log.V(1).Info("Added finalizer")
		return nil
	}

	// Step 1: Resolve OCI image to get digest
	imageRef := osImage.Spec.Source.Reference
	// createImageSource does not need a platform, as OSImages are not multi-arch.
	// The OCI reference of OSImages is already resolved to a specific architecture.
	// (Different architectures of the same OS must be backed by different base images,
	// and therefore need different OSImage resources.)
	imageSource, err := createImageSource(nil)
	if err != nil {
		return fmt.Errorf("failed to create image source: %w", err)
	}
	ociImage, err := imageSource.Resolve(ctx, imageRef)
	if err != nil {
		return fmt.Errorf("failed to resolve OCI image %s: %w", imageRef, err)
	}
	ironcoreImage, err := ironcoreimage.ResolveImage(ctx, ociImage)
	if err != nil {
		return fmt.Errorf("failed to resolve IronCore image: %w", err)
	}
	if ironcoreImage.RootFS == nil {
		return fmt.Errorf("IronCore image %s has no rootfs", imageRef)
	}

	// Step 2: Get or create base image
	imageDigest := ociImage.Descriptor().Digest
	baseImageID := imageDigest.Encoded()
	snapshotID := imageDigest.Encoded()

	baseImage, err := r.imageStore.Get(ctx, baseImageID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to get base image: %w", err)
		}

		log.V(1).Info("Creating base image", "imageId", baseImageID)
		imageSize := uint64(ironcoreImage.RootFS.Descriptor().Size)
		baseImage = &providerapi.Image{
			Metadata: apiutils.Metadata{
				ID: baseImageID,
				Labels: map[string]string{
					"os-image-id": osImage.ID,
				},
			},
			Spec: providerapi.ImageSpec{
				Size:   imageSize,
				WWN:    "", // Base image doesn't need WWN
				Limits: providerapi.Limits{},
				Encryption: providerapi.EncryptionSpec{
					Type: providerapi.EncryptionTypeUnencrypted, // Base images are unencrypted
				},
			},
		}

		baseImage, err = createOrGet(ctx, log, r.imageStore, baseImage, "Base image created", "Base image already exists, fetching")
		if err != nil {
			return fmt.Errorf("failed to create or get base image: %w", err)
		}
	}

	// Step 3: Check base image state
	switch baseImage.Status.State {
	case providerapi.ImageStatePending:
		log.Info("Base image is pending, waiting", "imageId", baseImage.ID)
		return nil
	case providerapi.ImageStateAvailable:
		log.V(1).Info("Base image is available", "imageId", baseImage.ID)
	default:
		return fmt.Errorf("base image %s in unexpected state: %s", baseImage.ID, baseImage.Status.State)
	}

	// Step 4: Get or create snapshot of base image
	snapshot, err := r.snapshotStore.Get(ctx, snapshotID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to get snapshot %s: %w", snapshotID, err)
		}

		// Step 4.5: Populate base image before taking snapshot
		if err := r.populateBaseImage(ctx, log, baseImage, ironcoreImage); err != nil {
			return fmt.Errorf("failed to populate RBD base image: %w", err)
		}

		// Create snapshot
		log.V(1).Info("Creating snapshot of RBD base image", "snapshotId", snapshotID, "imageId", baseImageID)
		snapshot = &providerapi.Snapshot{
			Metadata: apiutils.Metadata{
				ID: snapshotID,
				Labels: map[string]string{
					"os-image-id": osImage.ID,
				},
			},
			Spec: providerapi.SnapshotSpec{
				ImageRef:   baseImageID,
				Protection: providerapi.SnapshotProtectionProtected, // Protection needed for cloning
			},
		}

		snapshot, err = createOrGet(ctx, log, r.snapshotStore, snapshot, "Snapshot created", "Snapshot already exists, fetching")
		if err != nil {
			return fmt.Errorf("failed to create or get snapshot: %w", err)
		}
	}

	// Step 5: Check snapshot state
	switch snapshot.Status.State {
	case providerapi.SnapshotStatePending:
		log.Info("Snapshot is pending, waiting", "snapshotId", snapshot.ID)
		return nil
	case providerapi.SnapshotStateReady:
		log.V(1).Info("Snapshot is ready", "snapshotId", snapshot.ID)
	case providerapi.SnapshotStateFailed:
		log.Info("Snapshot failed, recreating", "snapshotId", snapshot.ID)
		if err := r.snapshotStore.Delete(ctx, snapshot.ID); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("failed to delete snapshot %s: %w", snapshot.ID, err)
			}
		}
		log.V(1).Info("Deleted failed snapshot", "snapshotId", snapshot.ID)
		return nil
	default:
		return fmt.Errorf("snapshot %s in unexpected state: %s", snapshotID, snapshot.Status.State)
	}

	// Step 6: Update OS image state
	var snapshotRef string
	if osImage.Status.SnapshotRef != nil {
		snapshotRef = *osImage.Status.SnapshotRef
	} else {
		snapshotRef = ""
	}
	needsUpdate := osImage.Status.State != providerapi.OSImageStateAvailable ||
		osImage.Status.Size != snapshot.Status.Size ||
		snapshotRef != snapshot.ID

	if !needsUpdate {
		return nil
	}

	osImage.Status.State = providerapi.OSImageStateAvailable
	osImage.Status.Size = snapshot.Status.Size
	osImage.Status.SnapshotRef = &snapshot.ID

	if _, err = r.osImageStore.Update(ctx, osImage); err != nil {
		return fmt.Errorf("failed to update OS image metadata: %w", err)
	}
	return nil
}

func (r *OSImageReconciler) deleteOSImage(ctx context.Context, log logr.Logger, osImage *providerapi.OSImage) error {
	if !slices.Contains(osImage.Finalizers, osImageFinalizer) {
		log.V(1).Info("OS image has no finalizer: done")
		return nil
	}

	imageRef := osImage.Spec.Source.Reference
	// createImageSource does not need a platform, as OSImages are not multi-arch.
	imageSource, err := createImageSource(nil)
	if err != nil {
		return fmt.Errorf("failed to create image source: %w", err)
	}
	ociImage, err := imageSource.Resolve(ctx, imageRef)
	if err != nil {
		return fmt.Errorf("failed to resolve OCI image %s: %w", imageRef, err)
	}

	imageDigest := ociImage.Descriptor().Digest
	baseImageID := imageDigest.Encoded()
	snapshotID := imageDigest.Encoded()

	if err := r.snapshotStore.Delete(ctx, snapshotID); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to delete snapshot %s: %w", snapshotID, err)
		}
		log.V(1).Info("Snapshot already deleted", "snapshotId", snapshotID)
	} else {
		log.Info("Snapshot deleted", "snapshotId", snapshotID)
	}

	if err := r.imageStore.Delete(ctx, baseImageID); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to delete base image %s: %w", baseImageID, err)
		}
		log.V(1).Info("Base image already deleted", "imageId", baseImageID)
	} else {
		log.Info("Base image deleted", "imageId", baseImageID)
	}

	if _, removed, err := removeFinalizer(ctx, r.osImageStore, osImage, osImageFinalizer); store.IgnoreErrNotFound(err) != nil {
		return fmt.Errorf("failed to update OS image metadata: %w", err)
	} else if removed {
		log.V(1).Info("Removed finalizer")
	}
	return nil
}

// TODO: long running operation; move to background worker?
func (r *OSImageReconciler) populateBaseImage(ctx context.Context, log logr.Logger, baseImage *providerapi.Image, ironcoreImage *ironcoreimage.Image) error {
	ioCtx, err := r.conn.OpenIOContext(r.pool)
	if err != nil {
		return fmt.Errorf("unable to get RADOS IOContext: %w", err)
	}
	defer ioCtx.Destroy()

	rbdImage, err := librbd.OpenImage(ioCtx, baseImage.ID, librbd.NoSnapshot)
	if err != nil {
		if !errors.Is(err, librbd.ErrNotFound) {
			return fmt.Errorf("failed to open RBD base image: %w", err)
		}
		return fmt.Errorf("RBD base image not found: %w", err)
	}
	defer closeImage(log, rbdImage)

	// TODO: How to make sure population only happens once? state update?
	// TODO: how to make sure only one reconciler attempts population at a time?
	if err := copyRootFSToImage(ctx, log, ironcoreImage.RootFS, rbdImage, r.populatorBufferSize); err != nil {
		return fmt.Errorf("failed to populate RBD base image: %w", err)
	}

	return nil
}

func copyRootFSToImage(ctx context.Context, log logr.Logger, rootFS image.Layer, rbdImage *librbd.Image, bufferSize int64) error {
	content, err := rootFS.Content(ctx)
	if err != nil {
		return fmt.Errorf("could not get rootfs content: %w", err)
	}
	defer func() {
		if closeErr := content.Close(); closeErr != nil {
			log.Error(closeErr, "failed to close rootfs content reader")
		}
	}()
	size := rootFS.Descriptor().Size

	throughputReader := rater.NewRater(content)
	var written atomic.Int64
	log.Info("Populating RBD base image", "imageId", rbdImage.GetName(), "size", fmt.Sprintf("%d b", size), "written", fmt.Sprintf("%d b", written.Load()))

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				log.V(1).Info("Populating RBD base image", "imageId", rbdImage.GetName(), "rate", throughputReader.String(), "size", fmt.Sprintf("%d b", size), "written", fmt.Sprintf("%d b", written.Load()))
			case <-done:
				return
			}
		}
	}()
	defer func() { close(done) }()

	buffer := make([]byte, bufferSize)
	countWriter := &countingWriter{writer: rbdImage, written: &written}
	if _, err := io.CopyBuffer(countWriter, throughputReader, buffer); err != nil {
		return fmt.Errorf("failed to copy image contents onto RBD image: %w", err)
	}

	log.Info("Finished populating RBD base image", "imageId", rbdImage.GetName(), "size", fmt.Sprintf("%d b", size), "written", fmt.Sprintf("%d b", written.Load()))
	return nil
}

type countingWriter struct {
	writer  io.Writer
	written *atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.writer.Write(p)
	cw.written.Add(int64(n))
	return n, err
}
