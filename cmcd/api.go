// Copyright (c) 2021 Fraunhofer AISEC
// Fraunhofer-Gesellschaft zur Foerderung der angewandten Forschung e.V.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

// Install github packages with "go get [url]"
import (
	"context"
	"crypto"
	"crypto/rand"
	"errors"

	"encoding/hex"
	"encoding/json"

	log "github.com/sirupsen/logrus"

	// local modules
	ar "github.com/Fraunhofer-AISEC/cmc/attestationreport"
	ci "github.com/Fraunhofer-AISEC/cmc/cmcinterface"
	"github.com/Fraunhofer-AISEC/cmc/tpmdriver"
)

func (s *server) Attest(ctx context.Context, in *ci.AttestationRequest) (*ci.AttestationResponse, error) {

	log.Info("Prover: Generating Attestation Report with nonce: ", hex.EncodeToString(in.Nonce))

	tpmParams := ar.TpmParams{
		Nonce: in.Nonce,
		Pcrs:  s.pcrs,
		Certs: ar.TpmCerts{
			AkCert:        string(s.certs.Ak),
			Intermediates: []string{string(s.certs.DeviceSubCa)},
			CaCert:        string(s.certs.Ca),
		},
		UseIma: s.config.UseIma,
		ImaPcr: s.config.ImaPcr,
	}

	a := ar.Generate(in.Nonce, s.metadata, []ar.Measurement{s.tpm}, []ar.MeasurementParams{tpmParams})

	log.Info("Prover: Signing Attestation Report")
	tlsKeyPriv, tlsKeyPub, err := tpmdriver.GetTLSKey()
	if err != nil {
		log.Error("Prover: Failed to get TLS Key")
		return &ci.AttestationResponse{Status: ci.Status_FAIL}, nil
	}

	var status ci.Status
	certsPem := [][]byte{s.certs.TLSCert, s.certs.DeviceSubCa, s.certs.Ca}
	ok, data := ar.Sign(&s.tpm.Mu, a, tlsKeyPriv, tlsKeyPub, certsPem)
	if !ok {
		log.Error("Prover: Failed to sign Attestion Report ")
		status = ci.Status_FAIL
	} else {
		status = ci.Status_OK
	}

	log.Info("Prover: Finished")

	response := &ci.AttestationResponse{
		Status:            status,
		AttestationReport: data,
	}

	return response, nil
}

func (s *server) Verify(ctx context.Context, in *ci.VerificationRequest) (*ci.VerificationResponse, error) {

	var status ci.Status

	log.Info("Received Connection Request Type 'Verification Request'")

	log.Info("Verifier: Verifying Attestation Report")
	result := ar.Verify(string(in.AttestationReport), in.Nonce, s.certs.Ca, s.roles)

	log.Info("Verifier: Marshaling Attestation Result")
	data, err := json.Marshal(result)
	if err != nil {
		log.Errorf("Verifier: Failed to marshal Attestation Result: %v", err)
		status = ci.Status_FAIL
	} else {
		status = ci.Status_OK
	}

	response := &ci.VerificationResponse{
		Status:             status,
		VerificationResult: data,
	}

	log.Info("Verifier: Finished")

	return response, nil
}

func (s *server) TLSSign(ctx context.Context, in *ci.TLSSignRequest) (*ci.TLSSignResponse, error) {
	var err error
	var sr *ci.TLSSignResponse
	var opts crypto.SignerOpts
	var signature []byte
	var tlsKeyPriv crypto.PrivateKey

	// get sign opts
	opts, err = convertHash(in.GetHashtype(), in.GetPssOpts())
	if err != nil {
		log.Error("[Prover] Failed to choose requested hash function.", err.Error())
		return &ci.TLSSignResponse{Status: ci.Status_FAIL}, errors.New("Prover: Failed to find appropriate hash function")
	}
	// get key
	tlsKeyPriv, _, err = tpmdriver.GetTLSKey()
	if err != nil {
		log.Error("[Prover] Failed to get TLS key. ", err.Error())
		return &ci.TLSSignResponse{Status: ci.Status_FAIL}, errors.New("Prover: Failed to get TLS key")
	}
	// Sign
	// Convert crypto.PrivateKey to crypto.Signer
	log.Trace("[Prover] TLSSign using opts: ", opts)
	signature, err = tlsKeyPriv.(crypto.Signer).Sign(rand.Reader, in.GetContent(), opts)
	if err != nil {
		log.Error("[Prover] ", err.Error())
		return &ci.TLSSignResponse{Status: ci.Status_FAIL}, errors.New("Prover: Failed to perform Signing operation")
	}
	// Create response
	sr = &ci.TLSSignResponse{
		Status:        ci.Status_OK,
		SignedContent: signature,
	}
	// Return response
	log.Info("Prover: Performed Sign operation.")
	return sr, nil
}

// Loads public key for tls certificate
func (s *server) TLSCert(ctx context.Context, in *ci.TLSCertRequest) (*ci.TLSCertResponse, error) {
	var resp *ci.TLSCertResponse = &ci.TLSCertResponse{}
	if s.certs.TLSCert == nil {
		log.Error("Prover: TLS Certificate not found - was the device provisioned correctly?")
		return &ci.TLSCertResponse{Status: ci.Status_FAIL}, errors.New("No TLS Certificate obtained")
	}
	// provide TLS certificate chain
	resp.Certificate = [][]byte{s.certs.TLSCert, s.certs.DeviceSubCa}
	resp.Status = ci.Status_OK
	log.Info("Prover: Obtained TLS Cert.")
	return resp, nil
}