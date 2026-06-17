package main

import (
	"crypto/tls"
	"net/http"
)

func main() {
    // Disable TLS verification to work around "certificate is expired"
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
}
