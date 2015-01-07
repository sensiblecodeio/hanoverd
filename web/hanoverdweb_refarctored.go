package web

import (
	"code.google.com/p/go.net/websocket"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

/*
	Start web listener from outside, args:
		[string]address 	-> 	where should it listen in format host:port, ex.: localhost:4000
		[boolean]debug 		-> 	should we turn on the debug mode?
*/

/*
	Wannabe data structures
*/

/*
	Log:
		[float64]tp 	-> 	type of message
		[string]msg 	-> 	message
		[string]cont 	-> 	container id

	log types:
		0, normal "just sayin"
*/

type Log struct {
	tp float64
	msg string
	cont string
}

/*
	Status:
		[float64]tp 	-> 	type of status
		[string]cont 	-> 	a container 
	Status types:
		@0 	-> 	container started
		@1 	-> 	container stopped
		@2 	-> 	container active
		@3 	-> 	container not active
*/

type Status struct {
	tp float64,
	cont string
}

func startWebListener(string address, boolean debug) {

}

func dbg(event string, data interface{}){
	if(debug == true) {
		log.PrintLn(time.Now(), event, data)
	}
}


var debug = true
dbg("lol", 1313)