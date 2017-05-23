package main

import (
	"code.google.com/p/go.net/websocket"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
	"encoding/json"
)

const (
	listenAddr = "localhost:4000" // server address
)

var (
	pwd, _        = os.Getwd()
	RootTemp      = template.Must(template.ParseFiles(pwd + "/static/index.html"))
	JSON          = websocket.JSON           // codec for JSON
	Message       = websocket.Message        // codec for string, []byte
	ActiveClients = make(map[ClientConn]int) // map containing clients
)

// Initialize handlers and websocket handlers
func init() {
	http.HandleFunc("/", RootHandler)
	http.Handle("/sock", websocket.Handler(SockServer))

	/*
		Static css and etc asset handler.
	*/
	http.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		log.Println("about to serve", r.URL.Path)
		http.ServeFile(w, r, r.URL.Path[1:])
	})
}

// Client connection consists of the websocket and the client ip
type ClientConn struct {
	websocket *websocket.Conn
	clientIP  string
}

/*
	Websocket server
*/
func SockServer(ws *websocket.Conn) {
	var err error

	/*
		Cleanup on the end.
	*/
	defer func() {
		if err = ws.Close(); err != nil {
			log.Println("Websocket could not be closed", err.Error())
		}
		log.Println("websocket closed")
	}()

	client := ws.Request().RemoteAddr
	log.Println("Client connected:", client)
	sockCli := ClientConn{ws, client}
	ActiveClients[sockCli] = 0
	log.Println("Number of clients connected ...", len(ActiveClients))


	events := make(chan [5]int)
	// for loop so the websocket stays open otherwise
	// it'll close after one Receieve and Send
	go func(){
		for {
			/*
				We wait till an event comes, then json encode it.
			*/
			clientMessage := <-events 
			jsonEncoded, err := json.Marshal(clientMessage)
			msgToSend := string(jsonEncoded)
			log.Println(jsonEncoded, msgToSend, clientMessage)
			/*
				If everything went okay wit the encoding, we'll send it to the
				connected clients.
			*/
			if err == nil {
				for cs, _ := range ActiveClients {
					if err = Message.Send(cs.websocket, msgToSend); err != nil {
						// we could not send the message to a peer
						log.Println("Could not send message to ", cs.clientIP, err.Error())
					}
				}
			} else {
				log.Println("Bad news from encoding.", err.Error())

			}
		}
	}()

	for _ = range time.Tick(2 * time.Second) { 
         events <- [5]int{ 98, 93, 77, 82, 83 }
    } 

}

// RootHandler renders the template for the root page
func RootHandler(w http.ResponseWriter, req *http.Request) {
	err := RootTemp.Execute(w, listenAddr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	err := http.ListenAndServe(listenAddr, nil)
	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}
