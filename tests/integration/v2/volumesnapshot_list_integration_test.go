// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"

	apiv2 "github.com/ironcore-dev/ceph-provider/api/v2"
	"github.com/ironcore-dev/ceph-provider/internal/omap"
	metav1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	iriv1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("List VolumeSnapshot", func() {
	It("should list volume snapshots", func(ctx SpecContext) {
		By("creating a volume")
		createVolumeResp, err := volumeClient.CreateVolume(ctx, &iriv1alpha1.CreateVolumeRequest{
			Volume: &iriv1alpha1.Volume{
				Metadata: &metav1alpha1.ObjectMetadata{
					Id:     "foo",
					Labels: map[string]string{"foo": "bar"},
				},
				Spec: &iriv1alpha1.VolumeSpec{
					Class: "foo",
					Resources: &iriv1alpha1.VolumeResources{
						StorageBytes: 1024 * 1024 * 1024,
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		volumeId := createVolumeResp.Volume.Metadata.Id
		By("ensuring the volume has been created in volumes store")
		volume := &apiv2.Volume{}
		Eventually(func() *apiv2.Volume {
			oMap, err := ioctx.GetOmapValues(omap.NameVolumes, "", volumeId, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(oMap).To(HaveKey(volumeId))
			Expect(json.Unmarshal(oMap[volumeId], volume)).NotTo(HaveOccurred())
			return volume
		}).Should(SatisfyAll(
			HaveField("Metadata.ID", Equal(volumeId)),
			HaveField("Status.State", Equal(apiv2.VolumeStateAvailable)),
		))

		DeferCleanup(volumeClient.DeleteVolume, &iriv1alpha1.DeleteVolumeRequest{
			VolumeId: volumeId,
		})

		By("creating a volume snapshot for volume")
		createVolumeSnapshotResp, err := volumeClient.CreateVolumeSnapshot(ctx, &iriv1alpha1.CreateVolumeSnapshotRequest{
			VolumeSnapshot: &iriv1alpha1.VolumeSnapshot{
				Metadata: &metav1alpha1.ObjectMetadata{
					Id:     "foo",
					Labels: map[string]string{"foo": "bar"},
				},
				Spec: &iriv1alpha1.VolumeSnapshotSpec{
					VolumeId: createVolumeResp.Volume.Metadata.Id,
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		snapshotID := createVolumeSnapshotResp.VolumeSnapshot.Metadata.Id
		By("ensuring snapshot has been created in snapshot store")
		snapshot := &apiv2.Snapshot{}
		Eventually(ctx, func() *apiv2.Snapshot {
			oMap, err := ioctx.GetOmapValues(omap.NameSnapshots, "", snapshotID, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(oMap).To(HaveKey(snapshotID))
			Expect(json.Unmarshal(oMap[snapshotID], snapshot)).NotTo(HaveOccurred())
			return snapshot
		}).Should(SatisfyAll(
			HaveField("Metadata.ID", Equal(snapshotID)),
			HaveField("Status.State", Equal(apiv2.SnapshotStateReady)),
		))

		By("listing volume snapshots with volume snapshot id")
		Eventually(func() *iriv1alpha1.VolumeSnapshotStatus {
			resp, err := volumeClient.ListVolumeSnapshots(ctx, &iriv1alpha1.ListVolumeSnapshotsRequest{
				Filter: &iriv1alpha1.VolumeSnapshotFilter{
					Id: createVolumeSnapshotResp.VolumeSnapshot.Metadata.Id,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.VolumeSnapshots).NotTo(BeEmpty())
			Expect(resp.VolumeSnapshots).To(HaveLen(1))
			return resp.VolumeSnapshots[0].Status
		}).Should(SatisfyAll(
			HaveField("State", Equal(iriv1alpha1.VolumeSnapshotState_VOLUME_SNAPSHOT_READY)),
			HaveField("Size", Equal(int64(1024*1024*1024))),
		))

		DeferCleanup(volumeClient.DeleteVolumeSnapshot, &iriv1alpha1.DeleteVolumeSnapshotRequest{
			VolumeSnapshotId: createVolumeSnapshotResp.VolumeSnapshot.Metadata.Id,
		})

		By("creating another volume snapshot for volume")
		createVolumeSnapshotResp, err = volumeClient.CreateVolumeSnapshot(ctx, &iriv1alpha1.CreateVolumeSnapshotRequest{
			VolumeSnapshot: &iriv1alpha1.VolumeSnapshot{
				Metadata: &metav1alpha1.ObjectMetadata{
					Id:     "foo1",
					Labels: map[string]string{"foo": "bar"},
				},
				Spec: &iriv1alpha1.VolumeSnapshotSpec{
					VolumeId: createVolumeResp.Volume.Metadata.Id,
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		snapshotID = createVolumeSnapshotResp.VolumeSnapshot.Metadata.Id
		By("ensuring snapshot has been created in snapshot store")
		snapshot1 := &apiv2.Snapshot{}
		Eventually(ctx, func() *apiv2.Snapshot {
			oMap, err := ioctx.GetOmapValues(omap.NameSnapshots, "", snapshotID, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(oMap).To(HaveKey(snapshotID))
			Expect(json.Unmarshal(oMap[snapshotID], snapshot1)).NotTo(HaveOccurred())
			return snapshot1
		}).Should(SatisfyAll(
			HaveField("Metadata.ID", Equal(snapshotID)),
			HaveField("Status.State", Equal(apiv2.SnapshotStateReady)),
		))

		DeferCleanup(volumeClient.DeleteVolumeSnapshot, &iriv1alpha1.DeleteVolumeSnapshotRequest{
			VolumeSnapshotId: createVolumeSnapshotResp.VolumeSnapshot.Metadata.Id,
		})

		By("listing volume snapshots with label selector")
		Eventually(func() {
			resp, err := volumeClient.ListVolumeSnapshots(ctx, &iriv1alpha1.ListVolumeSnapshotsRequest{
				Filter: &iriv1alpha1.VolumeSnapshotFilter{
					LabelSelector: map[string]string{"foo": "bar"},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.VolumeSnapshots).NotTo(BeEmpty())
			Expect(resp.VolumeSnapshots).To(HaveLen(2))
		})

		By("listing volume snapshots with wrong label selector")
		Eventually(func() {
			resp, err := volumeClient.ListVolumeSnapshots(ctx, &iriv1alpha1.ListVolumeSnapshotsRequest{
				Filter: &iriv1alpha1.VolumeSnapshotFilter{
					LabelSelector: map[string]string{"foo": "foo"},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.VolumeSnapshots).To(BeEmpty())
			Expect(resp.VolumeSnapshots).To(HaveLen(0))
		})
	})
})
