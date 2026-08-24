// preview-server is a credential-free Docker integration fixture.
package main

import (
	"fmt"
	"net/http"
)

func main() {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.Header().Set("X-Preview-Fixture", "true")
		_, _ = fmt.Fprintf(writer, "preview %s", request.URL.RequestURI())
	})
	if err := http.ListenAndServe("127.0.0.1:3456", handler); err != nil {
		panic(err)
	}
}
