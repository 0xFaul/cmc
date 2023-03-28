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

//go:build !nodefaults || socket

package main

import (
	"crypto"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"encoding/hex"
	"encoding/json"

	"github.com/fxamacker/cbor/v2"

	// local modules
	"github.com/Fraunhofer-AISEC/cmc/api"
	ar "github.com/Fraunhofer-AISEC/cmc/attestationreport"
	"github.com/Fraunhofer-AISEC/cmc/internal"
)

// Server is the server structure
type SocketServer struct{}

func init() {
	log.Trace("Adding unix domain socket server to supported servers")
	servers["socket"] = SocketServer{}
}

func (s SocketServer) Serve(addr string, conf *ServerConfig) error {

	log.Infof("Starting CMC %v server on %v", conf.Network, addr)

	socket, err := net.Listen(conf.Network, addr)
	if err != nil {
		return fmt.Errorf("failed to listen on unix domain soket: %w", err)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		os.Remove(addr)
		os.Exit(1)
	}()

	for {
		conn, err := socket.Accept()
		if err != nil {
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		go handleIncoming(conn, conf)
	}
}

func handleIncoming(conn net.Conn, conf *ServerConfig) {
	defer conn.Close()

	payload, reqType, err := api.Receive(conn)
	if err != nil {
		api.SendError(conn, "Failed to receive: %v", err)
		return
	}

	// Handle request
	switch reqType {
	case api.TypeAttest:
		attest(conn, payload, conf)
	case api.TypeVerify:
		verify(conn, payload, conf)
	case api.TypeTLSCert:
		tlscert(conn, payload, conf)
	case api.TypeTLSSign:
		tlssign(conn, payload, conf)
	default:
		api.SendError(conn, "Invalid Type: %v", reqType)
	}
}

func attest(conn net.Conn, payload []byte, conf *ServerConfig) {

	log.Debug("Prover: Received attestation request")

	req := new(api.AttestationRequest)
	err := cbor.Unmarshal(payload, req)
	if err != nil {
		api.SendError(conn, "failed to unmarshal attestation request: %v", err)
		return
	}

	log.Debugf("Prover: Generating Attestation Report with nonce: %v", hex.EncodeToString(req.Nonce))

	report, err := ar.Generate(req.Nonce, conf.Metadata, conf.MeasurementInterfaces, conf.Serializer)
	if err != nil {
		api.SendError(conn, "failed to generate attestation report: %v", err)
		return
	}

	if conf.Signer == nil {
		api.SendError(conn, "Failed to sign attestation report: No valid signer specified in config")
		return
	}

	log.Debug("Prover: Signing Attestation Report")
	r, err := ar.Sign(report, conf.Signer, conf.Serializer)
	if err != nil {
		api.SendError(conn, "Failed to sign attestation report: %v", err)
		return
	}

	// Serialize payload
	resp := &api.AttestationResponse{
		AttestationReport: r,
	}
	data, err := cbor.Marshal(resp)
	if err != nil {
		api.SendError(conn, "failed to marshal message: %v", err)
		return
	}

	api.Send(conn, data, api.TypeAttest)

	log.Debug("Prover: Finished")
}

func verify(conn net.Conn, payload []byte, conf *ServerConfig) {

	log.Debug("Received Connection Request Type 'Verification Request'")

	req := new(api.VerificationRequest)
	err := cbor.Unmarshal(payload, req)
	if err != nil {
		api.SendError(conn, "Failed to unmarshal verification request: %v", err)
		return
	}

	log.Debug("Verifier: Verifying Attestation Report")
	result := ar.Verify(string(req.AttestationReport), req.Nonce, req.Ca, req.Policies,
		conf.PolicyEngineSelect, conf.Serializer)

	log.Debug("Verifier: Marshaling Attestation Result")
	r, err := json.Marshal(result)
	if err != nil {
		api.SendError(conn, "Verifier: failed to marshal Attestation Result: %v", err)
		return
	}

	// Serialize payload
	resp := api.VerificationResponse{
		VerificationResult: r,
	}
	data, err := cbor.Marshal(&resp)
	if err != nil {
		api.SendError(conn, "failed to marshal message: %v", err)
		return
	}

	api.Send(conn, data, api.TypeVerify)

	log.Debug("Verifier: Finished")
}

func tlssign(conn net.Conn, payload []byte, conf *ServerConfig) {

	log.Debug("Received TLS sign request")

	// Parse the message and return the TLS signing request
	req := new(api.TLSSignRequest)
	err := cbor.Unmarshal(payload, req)
	if err != nil {
		api.SendError(conn, "failed to unmarshal payload: %v", err)
		return
	}

	// Get signing options from request
	opts, err := api.HashToSignerOpts(req.Hashtype, req.PssOpts)
	if err != nil {
		api.SendError(conn, "failed to choose requested hash function: %v", err)
		return
	}

	// Get key handle from (hardware) interface
	tlsKeyPriv, _, err := conf.Signer.GetSigningKeys()
	if err != nil {
		api.SendError(conn, "failed to get IK: %v", err)
		return
	}

	// Sign
	log.Trace("TLSSign using opts: ", opts)
	signature, err := tlsKeyPriv.(crypto.Signer).Sign(rand.Reader, req.Content, opts)
	if err != nil {
		api.SendError(conn, "failed to sign: %v", err)
		return
	}

	// Create response
	resp := &api.TLSSignResponse{
		SignedContent: signature,
	}
	data, err := cbor.Marshal(&resp)
	if err != nil {
		api.SendError(conn, "failed to marshal message: %v", err)
		return
	}

	api.Send(conn, data, api.TypeTLSSign)

	log.Debug("Performed signing")
}

func tlscert(conn net.Conn, payload []byte, conf *ServerConfig) {

	log.Debug("Received TLS cert request")

	// Parse the message and return the TLS signing request
	req := new(api.TLSSignRequest)
	err := cbor.Unmarshal(payload, req)
	if err != nil {
		api.SendError(conn, "failed to unmarshal payload: %v", err)
		return
	}
	// TODO ID is currently not used
	log.Tracef("Received TLS cert request with ID %v", req.Id)

	// Retrieve certificates
	certChain := conf.Signer.GetCertChain()

	// Create response
	resp := &api.TLSCertResponse{
		Certificate: internal.WriteCertsPem(certChain),
	}
	data, err := cbor.Marshal(&resp)
	if err != nil {
		api.SendError(conn, "failed to marshal message: %v", err)
		return
	}

	api.Send(conn, data, api.TypeTLSCert)

	log.Debug("Obtained TLS cert")
}