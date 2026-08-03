package main

import "testing"

func TestServeURLs(t *testing.T) {
	cases := []struct {
		addr, public, agent, resolved string
	}{
		{"127.0.0.1:8080", "", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"0.0.0.0:8080", "", "http://127.0.0.1:8080", ""},
		{":8080", "", "http://127.0.0.1:8080", ""},
		{"::", "", "http://127.0.0.1:8080", ""},
		{"192.168.1.10:8080", "", "http://127.0.0.1:8080", "http://192.168.1.10:8080"},
		{"0.0.0.0:8080", "http://cluster.example:8080", "http://127.0.0.1:8080", "http://cluster.example:8080"},
	}
	for _, c := range cases {
		agent, resolved := serveURLs(c.addr, c.public)
		if agent != c.agent || resolved != c.resolved {
			t.Errorf("serveURLs(%q, %q) = (%q, %q), want (%q, %q)", c.addr, c.public, agent, resolved, c.agent, c.resolved)
		}
	}
}
