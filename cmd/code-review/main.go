package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/sevigo/goframe/httpclient"
)

func main() {
	headerTimeout := 15 * time.Minute
	client := httpclient.NewClient(httpclient.NewConfig(
		httpclient.WithResponseHeaderTimeout(headerTimeout),
	))

	t, ok := client.Transport.(*http.Transport)
	if !ok {
		fmt.Println("transport is not *http.Transport")
		return
	}
	fmt.Printf("ResponseHeaderTimeout: %v\n", t.ResponseHeaderTimeout)
}
