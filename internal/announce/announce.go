// Package announce says on the local network, over Bonjour, that a Relay is
// here (_q8._tcp), so Q8's Connect to Relay… lists it. From a container it
// reaches the network only with network_mode: host (or macvlan); otherwise
// Q8 takes a typed address.
package announce

import (
	"fmt"

	"github.com/grandcat/zeroconf"
)

// Service is the Bonjour type Q8 looks for.
const Service = "_q8._tcp"

// Start announces name on port, with Relay's ID and protocol in its TXT
// record. Call the returned function to stop.
func Start(name, id string, port, protocol int) (func(), error) {
	txt := []string{"id=" + id, fmt.Sprintf("protocol=%d", protocol)}
	server, err := zeroconf.Register(name, Service, "local.", port, txt, nil)
	if err != nil {
		return func() {}, err
	}
	return server.Shutdown, nil
}
