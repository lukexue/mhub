package broker

import (
	"math/rand"
	"sync"
)

// all clients in mem, key is client id
var clients map[string]*incomingConn = make(map[string]*incomingConn, 10000)
var clientsMu sync.Mutex

// A random number generator ready to make client-id's, if
// they do not provide them to us.
var clientIdRand *rand.Rand
