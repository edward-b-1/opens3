package integration

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
)

// dumpTransport logs requests and responses when OPENS3_TEST_DEBUG is set.
type dumpTransport struct{ rt http.RoundTripper }

func (d dumpTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if os.Getenv("OPENS3_TEST_DEBUG") == "" {
		return d.rt.RoundTrip(r)
	}
	b, _ := httputil.DumpRequestOut(r, false)
	fmt.Fprintf(os.Stderr, ">>> %s\n", b)
	resp, err := d.rt.RoundTrip(r)
	if resp != nil {
		rb, _ := httputil.DumpResponse(resp, true)
		fmt.Fprintf(os.Stderr, "<<< %s\n", rb)
	}
	return resp, err
}
