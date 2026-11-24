// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package volumeserver

import (
	"context"
	"fmt"

	"github.com/ironcore-dev/ceph-provider/api"
	"github.com/ironcore-dev/ceph-provider/internal/utils"
	iri "github.com/ironcore-dev/ironcore/iri/apis/volume/v1alpha1"
)

func (s *Server) RotateVolumeEncryption(ctx context.Context, req *iri.RotateVolumeEncryptionRequest) (*iri.RotateVolumeEncryptionResponse, error) {
	log := s.loggerFrom(ctx, "VolumeID", req.GetVolumeId())
	log.V(1).Info("Rotating volume encryption key")

	encryption := req.Encryption
	if encryption == nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("encryption spec is required"))
	}
	passphrase, found := encryption.SecretData[EncryptionSecretDataPassphraseKey]
	if !found {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("secret data with key %q missing", EncryptionSecretDataPassphraseKey))
	}

	encryptedPassphrase, err := s.keyEncryption.Encrypt(passphrase)
	if err != nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("failed to encrypt passphrase: %w", err))
	}

	image, err := s.imageStore.Get(ctx, req.VolumeId)
	if err != nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("failed to get image: %w", err))
	}

	image.Spec.Encryption = &api.EncryptionSpec{
		Type:                api.EncryptionTypeEncrypted,
		EncryptedPassphrase: encryptedPassphrase,
	}

	if _, err := s.imageStore.Update(ctx, image); err != nil {
		return nil, utils.ConvertInternalErrorToGRPC(fmt.Errorf("failed to update image: %w", err))
	}

	log.V(1).Info("Volume encryption key rotated")
	return &iri.RotateVolumeEncryptionResponse{}, nil
}
