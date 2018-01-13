package packets

import (
	"bytes"
	"fmt"
	"net"
	"sync"

	bnet "github.com/evilsocket/bettercap-ng/net"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
)

type Activity struct {
	IP     net.IP
	MAC    net.HardwareAddr
	Source bool
}

type Traffic struct {
	Sent     uint64
	Received uint64
}

type Queue struct {
	iface  *bnet.Endpoint
	handle *pcap.Handle
	source *gopacket.PacketSource
	lock   *sync.Mutex
	active bool `json:"-"`

	Activities  chan Activity `json:"-"`
	Sent        uint64
	Received    uint64
	PktReceived uint64
	Errors      uint64
	Protos      map[string]uint64
	Traffic     map[string]*Traffic
}

func NewQueue(iface *bnet.Endpoint) (*Queue, error) {
	var err error

	q := &Queue{
		iface:       iface,
		handle:      nil,
		lock:        &sync.Mutex{},
		active:      true,
		source:      nil,
		Sent:        0,
		Received:    0,
		PktReceived: 0,
		Errors:      0,
		Protos:      make(map[string]uint64),
		Traffic:     make(map[string]*Traffic),
		Activities:  make(chan Activity),
	}

	q.handle, err = pcap.OpenLive(iface.Name(), 1024, true, pcap.BlockForever)
	if err != nil {
		return nil, err
	}

	q.source = gopacket.NewPacketSource(q.handle, q.handle.LinkType())
	go q.worker()

	return q, nil
}

func (q *Queue) worker() {
	for pkt := range q.source.Packets() {
		if q.active == false {
			return
		}
		pktSize := uint64(len(pkt.Data()))

		q.PktReceived++
		q.Received += pktSize

		// gather protocols stats
		pktLayers := pkt.Layers()
		for _, layer := range pktLayers {
			proto := layer.LayerType().String()
			if proto == "DecodeFailure" || proto == "Payload" {
				continue
			}

			if _, found := q.Protos[proto]; found == false {
				q.Protos[proto] = 0
			}
			q.Protos[proto] += 1
		}

		// check for new ipv4 endpoints
		leth := pkt.Layer(layers.LayerTypeEthernet)
		lip4 := pkt.Layer(layers.LayerTypeIPv4)

		if leth != nil && lip4 != nil {
			eth := leth.(*layers.Ethernet)
			ip4 := lip4.(*layers.IPv4)

			if bytes.Compare(q.iface.IP, ip4.SrcIP) != 0 && q.iface.Net.Contains(ip4.SrcIP) {
				q.Activities <- Activity{
					IP:     ip4.SrcIP,
					MAC:    eth.SrcMAC,
					Source: true,
				}

				addr := ip4.SrcIP.String()
				if _, found := q.Traffic[addr]; found == false {
					q.Traffic[addr] = &Traffic{}
				}

				q.Traffic[addr].Sent += pktSize
			}

			if bytes.Compare(q.iface.IP, ip4.DstIP) != 0 && q.iface.Net.Contains(ip4.DstIP) {
				q.Activities <- Activity{
					IP:     ip4.DstIP,
					MAC:    eth.SrcMAC,
					Source: false,
				}

				addr := ip4.DstIP.String()
				if _, found := q.Traffic[addr]; found == false {
					q.Traffic[addr] = &Traffic{}
				}

				q.Traffic[addr].Received += pktSize
			}
		}
	}
}

func (q *Queue) Send(raw []byte) error {
	q.lock.Lock()
	defer q.lock.Unlock()

	if q.active {
		err := q.handle.WritePacketData(raw)
		if err != nil {
			q.Sent += uint64(len(raw))
		} else {
			q.Errors += 1
		}
		return err
	} else {
		return fmt.Errorf("Packet queue is not active.")
	}
}

func (q *Queue) Stop() {
	q.lock.Lock()
	defer q.lock.Unlock()

	q.handle.Close()
	q.active = false
}
