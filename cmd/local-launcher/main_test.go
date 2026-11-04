//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackendReadinessRequiresManagedGameLogic(t *testing.T) {
	for _, test := range []struct {
		body           string
		ready, failure bool
	}{
		{`{"app":"rf4-local-prototype-v1","status":"ready","gameProtocol":"reference-experimental"}`, true, false},
		{`{"app":"rf4-local-prototype-v1","status":"ready","gameProtocol":"bootstrap-only"}`, false, true},
		{`{"app":"other-app","status":"ready","gameProtocol":"reference-experimental"}`, false, true},
		{`not-json`, false, true},
	} {
		instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(test.body)) }))
		ready, err := backendReady(instance.Client(), instance.URL)
		instance.Close()
		if ready != test.ready || (err != nil) != test.failure {
			t.Fatalf("ready=%v error=%v for %s", ready, err, test.body)
		}
	}
}

func TestOwnedBackendUnavailableReturnsFailure(t *testing.T) {
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"app":"rf4-local-prototype-v1","status":"unavailable"}`))
	}))
	defer instance.Close()
	ready, err := backendReady(instance.Client(), instance.URL)
	if ready || err == nil || !strings.Contains(err.Error(), "未就绪") {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
}
