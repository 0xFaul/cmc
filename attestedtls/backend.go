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

package attestedtls

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	// local modules
	ci "github.com/Fraunhofer-AISEC/cmc/cmcinterface"
)

/***********************************************************/
/* Backend to CMC */

const (
	cmcAddressDefault = "localhost"
	cmcPortDefault    = "9955"
	timeoutSec        = 10
)

// Struct that holds information on cmc address and port
// to be used by Listener and DialConfig
type cmcConfig struct {
	cmcPort    string
	cmcAddress string
	ca         []byte
	policies   []byte
}

// Creates connection with cmcd deamon at specified address
func getCMCServiceConn(cc cmcConfig) (ci.CMCServiceClient, *grpc.ClientConn, context.CancelFunc) {
	log.Trace("Contacting cmcd on: " + cc.cmcAddress + ":" + cc.cmcPort)
	ctx, cancel := context.WithTimeout(context.Background(), timeoutSec*time.Second)
	conn, err := grpc.DialContext(ctx, cc.cmcAddress+":"+cc.cmcPort, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		log.Errorf("ERROR: did not connect: %v", err)
		cancel()
		return nil, nil, nil
	}

	log.Trace("Creating new service client")
	return ci.NewCMCServiceClient(conn), conn, cancel
}

/***********************************************************/
/* Backend between two connectors / client and connector */

// Writes byte array to provided channel by first sending length information, then data
func Write(msg []byte, c net.Conn) error {
	lenbuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenbuf, uint32(len(msg)))

	buf := append(lenbuf, msg...)

	_, err := c.Write(buf)

	return err
}

// Receives byte array from provided channel by first receiving length information, then data
func Read(c net.Conn) ([]byte, error) {
	lenbuf := make([]byte, 4)
	_, err := c.Read(lenbuf)

	if err != nil {
		return nil, fmt.Errorf("failed to receive message: no length: %v", err)
	}

	len := binary.BigEndian.Uint32(lenbuf) // Max size of 4GB
	log.Trace("TCP Message Length: ", len)

	if len == 0 {
		return nil, errors.New("message length is zero")
	}

	// Receive data in chunks of 1024 bytes as the Read function receives a maxium of 65536 bytes
	// and the buffer must be longer, then append it to the final buffer
	tmpbuf := make([]byte, 1024)
	buf := make([]byte, 0)
	rcvlen := uint32(0)

	for {
		n, err := c.Read(tmpbuf)
		rcvlen += uint32(n)
		if err != nil {
			return nil, fmt.Errorf("failed to receive message: %w", err)
		}
		buf = append(buf, tmpbuf[:n]...)

		// Abort as soon as we have read the expected data as signaled in the first 4 bytes
		// of the message
		if rcvlen == len {
			log.Trace("Received message")
			break
		}
	}
	return buf, nil
}
