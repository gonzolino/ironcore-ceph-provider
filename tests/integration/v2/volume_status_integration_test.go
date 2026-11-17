// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"strconv"

	apiv2 "github.com/ironcore-dev/ceph-provider/api/v2"
	"github.com/ironcore-dev/ceph-provider/internal/omap"
	metav1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	iriv1alpha1 "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume Status", func() {
	It("should get the supported volume class status", func(ctx SpecContext) {
		By("getting volume status")
		resp, err := volumeClient.Status(ctx, &iriv1alpha1.StatusRequest{})
		Expect(err).NotTo(HaveOccurred())

		size, err := strconv.Atoi(cephDiskSize)
		Expect(err).NotTo(HaveOccurred())

		By("validating volume class status")
		Expect(resp.VolumeClassStatus[0]).Should(SatisfyAll(
			HaveField("VolumeClass", Equal(&iriv1alpha1.VolumeClass{
				Name: "foo",
				Capabilities: &iriv1alpha1.VolumeClassCapabilities{
					Tps:  262144000,
					Iops: 15000,
				},
			})),
			// TODO: The pool size depends on the ceph setup in the integration test workflow.
			// We need to adjust/make the pool size configurable in the future.
			HaveField("Quantity", And(
				BeNumerically(">", int64((size/10)*9)),
				BeNumerically("<=", int64((size/10)*11)),
			)),
		))

		By("creating a volume with the given volume class")
		createResp, err := volumeClient.CreateVolume(ctx, &iriv1alpha1.CreateVolumeRequest{
			Volume: &iriv1alpha1.Volume{
				Metadata: &metav1alpha1.ObjectMetadata{
					Id: "foo",
				},
				Spec: &iriv1alpha1.VolumeSpec{
					Class: "foo",
					Resources: &iriv1alpha1.VolumeResources{
						StorageBytes: 1 * 1024,
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(createResp).Should(SatisfyAll(
			HaveField("Volume.Metadata.Id", Not(BeEmpty())),
			HaveField("Volume.Spec.Class", Equal("foo")),
		))

		By("ensuring correct iops and tps/bps in Ceph cluster volume specs")
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
			HaveField("Spec.Size", Equal(uint64(1*1024))),
			HaveField("Spec.Limits", SatisfyAll(
				HaveKeyWithValue(apiv2.ReadBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.WriteBPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.BPSLimit, int64(262144000)),
				HaveKeyWithValue(apiv2.ReadIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.WriteIOPSLimit, int64(15000)),
				HaveKeyWithValue(apiv2.IOPSLimit, int64(15000)),
			)),
		))

		DeferCleanup(volumeClient.DeleteVolume, &iriv1alpha1.DeleteVolumeRequest{
			VolumeId: createResp.Volume.Metadata.Id,
		})
	})
})
