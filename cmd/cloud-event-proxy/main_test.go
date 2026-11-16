package main

import "testing"

func TestAPIBaseURL(t *testing.T) {
	for _, tc := range []struct{ host, scheme, want string }{
		{"publisher.ns.svc", "https", "https://publisher.ns.svc:9043"},
		{"http://publisher-NODE_NAME.ns.svc:9043", "https", "https://publisher-worker.ns.svc:9043"},
		{"worker", "http", "http://worker:9043"},
		{"[::1]", "https", "https://[::1]:9043"},
	} {
		got, err := makeBaseURL(tc.host, "worker.example.com", 9043, tc.scheme)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %s, %v", tc.host, got, err)
		}
	}
}
