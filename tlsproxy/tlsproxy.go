//
// tlsproxy.go
//
// Copyright © 2015 Janne Snabb <snabb AT epipe.com>
//
// This file is part of Flixproxy.
//
// Flixproxy is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Flixproxy is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Flixproxy. If not, see <http://www.gnu.org/licenses/>.
//

package tlsproxy

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"

	"github.com/snabb/flixproxy/access"
	"github.com/snabb/flixproxy/util"
	"gopkg.in/inconshreveable/log15.v2"
)

type TLSProxy struct {
	config Config
	access access.Checker
	logger log15.Logger
}

type Config struct {
	Id           string
	Listen       string
	Acl          string
	Upstreamport string
	Upstreams    []string
	Fallback     string
	ProxyProto   bool
	SNIOnly      bool
	Deadline     int64
	Idle         int64
}

type replayConn struct {
	net.Conn
	replay *bytes.Buffer
}

func (c replayConn) Read(b []byte) (int, error) {
	if c.replay != nil && c.replay.Len() > 0 {
		return c.replay.Read(b)
	}
	return c.Conn.Read(b)
}

func New(config Config, access access.Checker, logger log15.Logger) (tlsProxy *TLSProxy) {
	if config.Id != "" {
		logger = logger.New("id", config.Id)
	}
	tlsProxy = &TLSProxy{
		config: config,
		access: access,
		logger: logger,
	}
	go util.ListenAndServe(tlsProxy.config.Listen, tlsProxy, logger)

	return tlsProxy
}

func (tlsProxy *TLSProxy) Stop() {
	// something
}

func (tlsProxy *TLSProxy) HandleConn(downstream *net.TCPConn) {
	defer downstream.Close()

	_ = util.SetDeadlineSeconds(downstream, tlsProxy.config.Deadline)

	logger := tlsProxy.logger.New("src", downstream.RemoteAddr())

	allowedProxy := tlsProxy.access.AllowedAddr(downstream.RemoteAddr())
	if !allowedProxy && tlsProxy.config.Fallback == "" {
		logger.Warn("access denied")
		return
	}

	firstByte := make([]byte, 1)
	_, err := io.ReadFull(downstream, firstByte)
	if err != nil {
		if netError, ok := err.(net.Error); ok && netError.Timeout() {
			logger.Info("timeout reading first byte")
		} else {
			logger.Info("error reading first byte", "err", err)
		}
		return
	}
	if firstByte[0] != 0x16 { // recordTypeHandshake
		logger.Warn("record type not handshake", "fistbyte", firstByte)
		return
	}

	versionBytes := make([]byte, 2)
	_, err = io.ReadFull(downstream, versionBytes)
	if err != nil {
		logger.Info("error reading version bytes", "err", err)
		return
	}
	if versionBytes[0] < 3 || (versionBytes[0] == 3 && versionBytes[1] < 1) {
		logger.Warn("SSL < 3.1 not supported", "versionbytes", versionBytes)
		return
	}

	restLengthBytes := make([]byte, 2)
	_, err = io.ReadFull(downstream, restLengthBytes)
	if err != nil {
		logger.Info("error reading restLength bytes", "err", err)
		return
	}
	restLength := int(restLengthBytes[0])<<8 + int(restLengthBytes[1])

	rest := make([]byte, restLength)
	_, err = io.ReadFull(downstream, rest)
	if err != nil {
		logger.Info("error reading rest of bytes", "err", err)
		return
	}
	if len(rest) == 0 || rest[0] != 1 { // typeClientHello
		logger.Warn("did not get ClientHello")
		return
	}

	m := new(clientHelloMsg)
	if !m.unmarshal(rest) {
		logger.Warn("error parsing ClientHello")
		return
	}
	peeked := append(append(append(firstByte, versionBytes...), restLengthBytes...), rest...)
	target := m.serverName + ":" + tlsProxy.config.Upstreamport
	proxyProtocol := false
	if m.serverName == "" {
		if tlsProxy.config.SNIOnly {
			c := replayConn{
				Conn:   downstream,
				replay: bytes.NewBuffer(peeked),
			}
			_ = tls.Server(c, &tls.Config{}).Handshake()
			return
		}
		if tlsProxy.config.Fallback == "" {
			logger.Error("upstream not allowed")
			return
		} else {
			target = tlsProxy.config.Fallback
			proxyProtocol = tlsProxy.config.ProxyProto
		}
	}

	logger = logger.New("upstream", target)

	proxyMatch := allowedProxy && util.ManyGlob(tlsProxy.config.Upstreams, target)
	if !proxyMatch {
		if tlsProxy.config.Fallback == "" {
			logger.Error("upstream not allowed")
			return
		} else {
			target = tlsProxy.config.Fallback
			proxyProtocol = tlsProxy.config.ProxyProto
			logger = logger.New("fallback", target)
		}
	}
	uaddr, err := net.ResolveTCPAddr("tcp", target)
	if err != nil {
		logger.Error("upstream address error", "err", err)
		return
	}
	upstream, err := net.DialTCP("tcp", nil, uaddr)
	if err != nil {
		logger.Error("error connecting to upstream", "err", err)
		return
	}
	defer upstream.Close()
	logger.Debug("connected to upstream")

	_ = util.SetDeadlineSeconds(upstream, tlsProxy.config.Deadline)

	// write proxy protocol to upstream
	if proxyProtocol {
		if err := util.WriteProxyProtocol(upstream, downstream); err != nil {
			logger.Error("error writing proxy protocol", "err", err)
			return
		}
	}
	if _, err = upstream.Write(peeked); err != nil {
		logger.Error("error writing to upstream", "err", err)
		return
	}
	// reset current deadlines
	_ = util.SetDeadlineSeconds(upstream, 0)
	_ = util.SetDeadlineSeconds(downstream, 0)

	util.Proxy(upstream, downstream, tlsProxy.config.Idle)
}

// eof
