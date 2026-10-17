// Package testutil holds the test plumbing go-serper's tests share: testdata
// fixtures and synthetic answer generators, HTTP fakes and scriptable bodies,
// a recording TLS server for HTTP/1.1 and HTTP/2, a scripted transport, a
// server that drops a request after reading it and a raw HTTP/2 server that
// faults one (to prove nothing is resent), a loopback-only dial guard, JSON
// quoting helpers, formatting-leak assertions and race-aware allocation
// guards.
//
// It is imported only by tests. It imports testify and the standard library,
// never the serper package, so that the root package's internal tests can use
// it too.
package testutil
