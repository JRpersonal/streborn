package upnp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A renderer that did not come up answers every SOAP call with 404 (#1582).
// OnNotFound fires for exactly that, and not for other failures.
func TestOnNotFoundFiresOn404Only(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	hits := 0
	r := &Renderer{ControlURL: srv.URL + "/AVTransport/Control", OnNotFound: func() { hits++ }}

	if err := r.SetURI(context.Background(), "http://192.0.2.1/stream", "x", ""); err == nil {
		t.Fatal("a 404 must still be an error")
	}
	if hits != 1 {
		t.Fatalf("OnNotFound ran %d times on a 404, want 1", hits)
	}
	status = http.StatusInternalServerError
	_ = r.Play(context.Background())
	if hits != 1 {
		t.Fatalf("OnNotFound ran on a 500")
	}
}
