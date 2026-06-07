package main

import (
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

func main() {
	http.HandleFunc("/orders", handle)
	http.ListenAndServe(":8080", nil)
}

func handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		fields := strings.Fields(string(msg))
		if len(fields) >= 2 {
			conn.Write(ctx, websocket.MessageText, []byte("ACK "+fields[1]))
		}
	}
}
