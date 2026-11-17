// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"fmt"
	"strings"

	apiv2 "github.com/ironcore-dev/ceph-provider/api/v2"
	"github.com/ironcore-dev/ceph-provider/internal/omap"
	metav1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	iriv1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
)

var _ = Describe("Expand Volume", func() {
	It("should expand a volume", func(ctx SpecContext) {
		By("creating a volume")
		createResp, err := volumeClient.CreateVolume(ctx, &iriv1alpha1.CreateVolumeRequest{
			Volume: &iriv1alpha1.Volume{
				Metadata: &metav1alpha1.ObjectMetadata{
					Id: "foo",
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
		By("ensuring the correct creation response")
		Expect(createResp).Should(SatisfyAll(
			HaveField("Volume.Metadata.Id", Not(BeEmpty())),
			HaveField("Volume.Spec.VolumeDataSource", BeNil()),
			HaveField("Volume.Spec.Class", Equal("foo")),
			HaveField("Volume.Spec.Resources.StorageBytes", Equal(int64(1024*1024*1024))),
			HaveField("Volume.Spec.Encryption", BeNil()),
			HaveField("Volume.Status.State", Equal(iriv1alpha1.VolumeState_VOLUME_PENDING)),
			HaveField("Volume.Status.Access", BeNil()),
		))

		DeferCleanup(volumeClient.DeleteVolume, &iriv1alpha1.DeleteVolumeRequest{
			VolumeId: createResp.Volume.Metadata.Id,
		})

		By("ensuring the correct volume has been created inside the ceph cluster")
		volume := &apiv2.Volume{}
		Eventually(func() *apiv2.Volume {
			oMap, err := ioctx.GetOmapValues(omap.NameVolumes, "", createResp.Volume.Metadata.Id, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(oMap).To(HaveKey(createResp.Volume.Metadata.Id))
			Expect(json.Unmarshal(oMap[createResp.Volume.Metadata.Id], volume)).NotTo(HaveOccurred())
			return volume
		}).Should(SatisfyAll(
			HaveField("Metadata.ID", Equal(createResp.Volume.Metadata.Id)),
			HaveField("Metadata.Labels", HaveKeyWithValue(apiv2.ClassLabel, "foo")),
			HaveField("Spec.Source.OSImage", BeNil()),
			HaveField("Spec.Source.Snapshot", BeNil()),
			HaveField("Spec.Size", Equal(uint64(1024*1024*1024))),
			HaveField("Spec.Limits", SatisfyAll(
				HaveKeyWithValue(apiv2.IOPSBurstDurationLimit, int64(15)),
				HaveKeyWithValue(apiv2.WriteIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.ReadBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.BPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.ReadIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.WriteBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.BPSBurstDurationLimit, int64(15)),
				HaveKeyWithValue(apiv2.IOPSLimit, int64(15000)),
			)),
			HaveField("Spec.VolumeEncryption.Type", Equal(apiv2.VolumeEncryptionType(""))),
			HaveField("Status.State", Equal(apiv2.VolumeStateAvailable)),
			HaveField("Status.Access", SatisfyAll(
				HaveField("Monitors", cephMonitors),
				HaveField("Handle", fmt.Sprintf("%s/%s%s", cephPoolname, apiv2.VolumeImageIDPrefix, createResp.Volume.Metadata.Id)),
				HaveField("User", strings.TrimPrefix(cephClientname, "client.")),
				HaveField("UserKey", Not(BeEmpty())),
			)),
			HaveField("Status.VolumeEncryption", Equal(apiv2.VolumeEncryptionState(""))),
		))

		By("ensuring volume is in available state and other state fields have been updated")
		Eventually(func() *iriv1alpha1.VolumeStatus {
			resp, err := volumeClient.ListVolumes(ctx, &iriv1alpha1.ListVolumesRequest{
				Filter: &iriv1alpha1.VolumeFilter{
					Id: createResp.Volume.Metadata.Id,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Volumes).NotTo(BeEmpty())
			return resp.Volumes[0].Status
		}).Should(SatisfyAll(
			HaveField("State", Equal(iriv1alpha1.VolumeState_VOLUME_AVAILABLE)),
			HaveField("Resources.StorageBytes", Equal(resource.NewQuantity(1024*1024*1024, resource.BinarySI).Value())),
			HaveField("Access", SatisfyAll(
				HaveField("Driver", "ceph"),
				HaveField("Handle", volume.Status.Access.WWN),
				HaveField("Attributes", SatisfyAll(
					HaveKeyWithValue("monitors", volume.Status.Access.Monitors),
					HaveKeyWithValue("image", volume.Status.Access.Handle),
				)),
				HaveField("SecretData", SatisfyAll(
					HaveKeyWithValue("userID", []byte(volume.Status.Access.User)),
					HaveKeyWithValue("userKey", []byte(volume.Status.Access.UserKey)),
				)),
			)),
		))

		By("expanding a volume")
		_, err = volumeClient.ExpandVolume(ctx, &iriv1alpha1.ExpandVolumeRequest{
			VolumeId: createResp.Volume.Metadata.Id,
			Resources: &iriv1alpha1.VolumeResources{
				StorageBytes: 2 * 1024 * 1024 * 1024,
			},
		})
		Expect(err).NotTo(HaveOccurred())

		By("ensuring volume size has been updated inside the ceph cluster after expand")
		Eventually(func() *apiv2.Volume {
			oMap, err := ioctx.GetOmapValues(omap.NameVolumes, "", createResp.Volume.Metadata.Id, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(oMap).To(HaveKey(createResp.Volume.Metadata.Id))
			Expect(json.Unmarshal(oMap[createResp.Volume.Metadata.Id], volume)).NotTo(HaveOccurred())
			return volume
		}).Should(SatisfyAll(
			HaveField("Metadata.ID", Equal(createResp.Volume.Metadata.Id)),
			HaveField("Metadata.Labels", HaveKeyWithValue(apiv2.ClassLabel, "foo")),
			HaveField("Spec.Source.OSImage", BeNil()),
			HaveField("Spec.Source.Snapshot", BeNil()),
			HaveField("Spec.Size", Equal(uint64(2*1024*1024*1024))),
			HaveField("Spec.Limits", SatisfyAll(
				HaveKeyWithValue(apiv2.IOPSBurstDurationLimit, int64(15)),
				HaveKeyWithValue(apiv2.WriteIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.ReadBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.BPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.ReadIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.WriteBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.BPSBurstDurationLimit, int64(15)),
				HaveKeyWithValue(apiv2.IOPSLimit, int64(15000)),
			)),
			HaveField("Spec.VolumeEncryption.Type", Equal(apiv2.VolumeEncryptionType(""))),
			HaveField("Status.State", Equal(apiv2.VolumeStateAvailable)),
			HaveField("Status.Access", SatisfyAll(
				HaveField("Monitors", cephMonitors),
				HaveField("Handle", fmt.Sprintf("%s/%s%s", cephPoolname, apiv2.VolumeImageIDPrefix, createResp.Volume.Metadata.Id)),
				HaveField("User", strings.TrimPrefix(cephClientname, "client.")),
				HaveField("UserKey", Not(BeEmpty())),
			)),
			HaveField("Status.VolumeEncryption", Equal(apiv2.VolumeEncryptionState(""))),
		))

		By("ensuring volume is in available state and other state fields have been updated")
		Eventually(func() *iriv1alpha1.VolumeStatus {
			resp, err := volumeClient.ListVolumes(ctx, &iriv1alpha1.ListVolumesRequest{
				Filter: &iriv1alpha1.VolumeFilter{
					Id: createResp.Volume.Metadata.Id,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Volumes).NotTo(BeEmpty())
			return resp.Volumes[0].Status
		}).Should(SatisfyAll(
			HaveField("State", Equal(iriv1alpha1.VolumeState_VOLUME_AVAILABLE)),
			HaveField("Resources.StorageBytes", Equal(resource.NewQuantity(2*1024*1024*1024, resource.BinarySI).Value())),
			HaveField("Access", SatisfyAll(
				HaveField("Driver", "ceph"),
				HaveField("Handle", volume.Status.Access.WWN),
				HaveField("Attributes", SatisfyAll(
					HaveKeyWithValue("monitors", volume.Status.Access.Monitors),
					HaveKeyWithValue("image", volume.Status.Access.Handle),
				)),
				HaveField("SecretData", SatisfyAll(
					HaveKeyWithValue("userID", []byte(volume.Status.Access.User)),
					HaveKeyWithValue("userKey", []byte(volume.Status.Access.UserKey)),
				)),
			)),
		))
	})
})
