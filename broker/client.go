package broker

import (
	crand "crypto/rand"
	"fmt"
	proto "github.com/funkygao/mqttmsg"
	"io"
	"log"
	"math/rand"
	"net"
	"strings"
	"time"
)

func init() {
	var seed int64
	var sb [4]byte
	crand.Read(sb[:])
	seed = int64(time.Now().Nanosecond())<<32 |
		int64(sb[0])<<24 | int64(sb[1])<<16 |
		int64(sb[2])<<8 | int64(sb[3])
	clientIdRand = rand.New(rand.NewSource(seed))
}

// A ClientConn holds all the state associated with a connection
// to an MQTT server. It should be allocated via NewClientConn.
type ClientConn struct {
	ClientId string // May be set before the call to Connect.
	conn     net.Conn
	Dump     bool                // When true, dump the messages in and out.
	Incoming chan *proto.Publish // Incoming messages arrive on this channel.

	out      chan job
	doneChan chan struct{}

	connack chan *proto.ConnAck
	suback  chan *proto.SubAck
}

// NewClientConn allocates a new ClientConn.
func NewClientConn(c net.Conn) *ClientConn {
	cc := &ClientConn{
		conn:     c,
		out:      make(chan job, clientQueueLength),
		Incoming: make(chan *proto.Publish, clientQueueLength),
		doneChan: make(chan struct{}),
		connack:  make(chan *proto.ConnAck),
		suback:   make(chan *proto.SubAck),
	}
	go cc.reader()
	go cc.writer()
	return cc
}

func (c *ClientConn) reader() {
	defer func() {
		// Cause the writer to exit.
		close(c.out)
		// Cause any goroutines waiting on messages to arrive to exit.
		close(c.Incoming)
		c.conn.Close()
	}()

	for {
		// TODO: timeout (first message and/or keepalives)
		m, err := proto.DecodeOneMessage(c.conn, nil)
		if err != nil {
			if err == io.EOF {
				return
			}
			if strings.HasSuffix(err.Error(), "use of closed network connection") {
				return
			}
			log.Print("cli reader: ", err)
			return
		}

		if c.Dump {
			log.Printf("dump  in: %T", m)
		}

		switch m := m.(type) {
		case *proto.Publish:
			c.Incoming <- m
		case *proto.PubAck:
			// ignore these
			continue
		case *proto.ConnAck:
			c.connack <- m
		case *proto.SubAck:
			c.suback <- m
		case *proto.Disconnect:
			return
		default:
			log.Printf("cli reader: got msg type %T", m)
		}
	}
}

func (c *ClientConn) writer() {
	// Close connection on exit in order to cause reader to exit.
	defer func() {
		// Signal to Disconnect() that the message is on its way, or
		// that the connection is closing one way or the other...
		close(c.doneChan)
	}()

	for job := range c.out {
		if c.Dump {
			log.Printf("dump out: %T", job.m)
		}

		// TODO: write timeout
		err := job.m.Encode(c.conn)
		if job.r != nil {
			close(job.r)
		}

		if err != nil {
			log.Print("cli writer: ", err)
			return
		}

		if _, ok := job.m.(*proto.Disconnect); ok {
			return
		}
	}
}

// Send the CONNECT message to the server. If the ClientId is not already
// set, use a default (a 63-bit decimal random number). The "clean session"
// bit is always set.
func (c *ClientConn) Connect(user, pass string) error {
	// TODO: Keepalive timer
	if c.ClientId == "" {
		c.ClientId = fmt.Sprint(clientIdRand.Int63())
	}
	req := &proto.Connect{
		ProtocolName:    "MQIsdp",
		ProtocolVersion: 3,
		ClientId:        c.ClientId,
		CleanSession:    true,
	}
	if user != "" {
		req.UsernameFlag = true
		req.PasswordFlag = true
		req.Username = user
		req.Password = pass
	}

	c.sync(req)
	ack := <-c.connack
	return ConnectionErrors[ack.ReturnCode]
}

// Sent a DISCONNECT message to the server. This function blocks until the
// disconnect message is actually sent, and the connection is closed.
func (c *ClientConn) Disconnect() {
	c.sync(&proto.Disconnect{})
	<-c.doneChan
}

// Subscribe subscribes this connection to a list of topics. Messages
// will be delivered on the Incoming channel.
func (c *ClientConn) Subscribe(tqs []proto.TopicQos) *proto.SubAck {
	c.sync(&proto.Subscribe{
		Header:    header(dupFalse, proto.QosAtLeastOnce, retainFalse),
		MessageId: 0,
		Topics:    tqs,
	})
	ack := <-c.suback
	return ack
}

// Publish publishes the given message to the MQTT server.
// The QosLevel of the message must be QosAtLeastOnce for now.
func (c *ClientConn) Publish(m *proto.Publish) {
	if m.QosLevel != proto.QosAtMostOnce {
		panic("unsupported QoS level")
	}
	c.out <- job{m: m}
}

// sync sends a message and blocks until it was actually sent.
func (c *ClientConn) sync(m proto.Message) {
	j := job{m: m, r: make(receipt)}
	c.out <- j
	<-j.r
	return
}
