// Package gateway is the plugin WebSocket server (ARCHITECTURE 3 and 5.3): the handshake (licence,
// tenant, server identity and secrets, duplicate installs, the PE_CONNECTIONS registration and the
// licence's server limit), batches to the event stream (acknowledged only after every PubAck),
// backpressure by delaying socket reads, heartbeats, signed outbound delivery, the pending verdict
// sweep, verdict acks, PlayerRisk, probe RTT statistics, /healthz and the public /status.
//
// One session (session.go) runs per WebSocket: a reader goroutine hands messages one at a time to
// the processing loop, so a message being processed or waiting for budget tokens stops the socket
// from being read. Outbound frames go through a bounded queue drained by a writer goroutine; a timer
// goroutine renews the connection registration, sweeps pending verdicts and watches for silence.
package gateway
