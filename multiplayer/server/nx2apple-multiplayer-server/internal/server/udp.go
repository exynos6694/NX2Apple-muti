package server

import (
	"encoding/json"
	"net"
	"time"
)

const maxUDPDatagram = 512

type udpMessage struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id"`
}

type udpReply struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id,omitempty"`
	Observed string `json:"observed,omitempty"`
}

func ServeRendezvousUDP(conn net.PacketConn, store *Store) {
	buf := make([]byte, maxUDPDatagram)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var msg udpMessage
		if json.Unmarshal(buf[:n], &msg) != nil || msg.Type != "register" || !validText(msg.ClientID, maxClientID) {
			continue
		}
		observed := addr.String()
		if store.SetUDPAddr(msg.ClientID, observed) != nil {
			continue
		}
		reply, _ := json.Marshal(udpReply{Type: "registered", ClientID: msg.ClientID, Observed: observed})
		_ = conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		_, _ = conn.WriteTo(reply, addr)
	}
}
