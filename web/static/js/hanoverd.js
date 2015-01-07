var hanoverd = new Object();
hanoverd.sock = null;

/*
	Connect and init.
*/

hanoverd.init = function(){
	window.hanoverd.setState(0);
	try {
		window.hanoverd.sock = new WebSocket(window.sockUrl);

		window.hanoverd.sock.onopen = function(m) { 
			console.log("CONNECTION opened..." + this.readyState);
			window.hanoverd.setState(1);
		}
		
		window.hanoverd.sock.onmessage = function(m) { 
			window.hanoverd.parseAndAddMessage(m.data);
		}
		
		window.hanoverd.sock.onerror = function(m) {
			window.hanoverd.setState(3);
		}
		
		window.hanoverd.sock.onclose = function(m) { 
			window.hanoverd.setState(3);
		}
	} catch(e) {
		window.hanoverd.setState(2);
		console.log(new Date(), '[init] catch(e):', e);
	}
}

/*
	Set the websocket state (connected/disconnected, etc.)
*/
hanoverd.setState = function(stateId){
	var states = [['Connecting...', 'label-primary'],['Connected', 'label-success'],['Error', 'label-danger'], ['Disconnected', 'label-default']]
	if(stateId < states.length) {
			$('.label-status')
				.removeClass('label-primary label-success label-danger')
				.addClass(states[stateId][1])
				.text(states[stateId][0]);
	} else {
		console.log(new Date(), '[setState] Invalid state', stateId);
	}
}

/*
	Parse and add message to the table
*/
hanoverd.parseAndAddMessage = function(data){
	data = JSON.parse(data);
}

/*
	On ready bind it and execute it.
*/

window.hanoverd = hanoverd;

$(document).ready(function(){
	window.hanoverd.init();
});
