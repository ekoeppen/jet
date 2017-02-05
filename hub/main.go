package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/mitchellh/mapstructure"
)

var (
	// the vers and date variables will be adjusted during Makefile builds
	vers, date = "v4.0", ""
	version    = vers + " " + date + " (" + runtime.Version() + ")"
)

func main() {
	adminFlag := flag.String("admin", "", "connect as admin to a running hub")
	dataStore := flag.String("data", "store.db", "data store file name & path")
	mqttPort := flag.String("mqtt", "tcp://localhost:1883", "MQTT server port")
	loggerDir := flag.String("logger", "logger", "dir path for logger files")
	packsDir := flag.String("packs", "packs", "location of all pack scripts")
	flag.Parse()

	// omit timestamps from the Log if $HOME is not set in the environment
	// works better when started from systemd, which adds its own timestamps
	if os.Getenv("HOME") == "" {
		log.SetFlags(log.Flags() & ^log.Ldate & ^log.Ltime)
	}

	// inform child processes (i.e. packs) about hub's version and mqtt port
	os.Setenv("HUB_VERSION", version)
	os.Setenv("HUB_MQTT", *mqttPort)

	// check for special admin mode, used by the "jet" wrapper script
	if *adminFlag != "" {
		connectToHub("admin", *adminFlag, false)
		adminCmd()
		return
	}

	// normal hub startup begins here, with a log entry
	log.Println("[JET/Hub] " + version)
	//log.Println("args:", os.Args[1:])

	// connect to MQTT and wait for it before doing anything else
	hubStatus := connectToHub("hub", *mqttPort, true)
	defer hub.Disconnect(250)

	// open the persistent data store
	if *dataStore != "" {
		db := dataStoreInit(*dataStore)
		defer db.Close()
		// start responding to data store requests
		go dataStoreListener("!/#")
		go dataFetchListener("@/#")
	}

	// save raw logger input to text files, one per day (UTC time)
	if *loggerDir != "" {
		go loggerSaveToDisk("logger/+/+", *loggerDir)
	}

	// copy each incoming "logger/<x>" message to "logger/<x>/<millis>"
	go loggerTimestamper("logger/+")

	// listen to serial device requests
	go serialProcessRequests("serial/+")

	// listen for JET pack setup requests
	if *packsDir != "" {
		go packsListener("packs/+", *packsDir)
	}

	// listen for web server setup requests
	go webListener("web/+")

	// send one message every second, on the second
	go startHeartbeat("hub/1hz")

	hubStatus <- 1 // hub is now fully initialised and running

	done := make(chan struct{})
	<-done // hang around forever
}

var hub mqtt.Client

// connectToHub sets up an MQTT client and registers as a "jet/..." client.
// Uses last-will to automatically unregister on disconnect. This returns a
// "topic notifier" channel to allow updating the registered status value.
func connectToHub(clientName, port string, retain bool) chan<- interface{} {
	// add a "fairly random" 6-digit suffix to make the client name unique
	nanos := time.Now().UnixNano()
	clientID := fmt.Sprintf("%s/%06d", clientName, nanos%1e6)

	options := mqtt.NewClientOptions()
	options.AddBroker(port)
	options.SetClientID(clientID)
	options.SetKeepAlive(30 * time.Second)
	options.SetCleanSession(true)
	options.SetMaxReconnectInterval(1 * time.Second)
	options.SetBinaryWill("jet/"+clientID, nil, 1, retain)
	hub = mqtt.NewClient(options)

	if t := hub.Connect(); t.Wait() && t.Error() != nil {
		log.Fatal(t.Error())
	}

	if retain {
		log.Println("connected as", clientID, "to", port)
	}

	// register as jet client, cleared on disconnect by the will
	feed := topicNotifier("jet/"+clientID, retain)
	feed <- 0 // start off with state "0" to indicate connection

	// return a topic feed to allow publishing hub status changes
	return feed
}

// sendToHub publishes a message, and waits for it to complete successfully.
// Note: does no JSON conversion if the payload is already a []byte.
func sendToHub(topic string, payload interface{}, retain bool) {
	data, ok := payload.([]byte)
	if !ok {
		var e error
		data, e = json.Marshal(payload)
		if e != nil {
			log.Println("json conversion failed:", e, payload)
			return
		}
	}
	t := hub.Publish(topic, 1, retain, data)
	if t.Wait() && t.Error() != nil {
		log.Print(t.Error())
	}
}

type event struct {
	Topic    string
	Payload  []byte
	Retained bool
}

func (e *event) Decode(result interface{}) bool {
	var payload interface{}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		log.Println("json decode error:", err, e.Payload)
		return false
	}
	if err := mapstructure.WeakDecode(payload, result); err != nil {
		log.Println("decode error:", err, e)
		return false
	}
	return true
}

// topicWatcher turns an MQTT subscription into a channel feed of events.
func topicWatcher(pattern string) <-chan event {
	feed := make(chan event)

	t := hub.Subscribe(pattern, 0, func(hub mqtt.Client, msg mqtt.Message) {
		feed <- event{
			Topic:    msg.Topic(),
			Payload:  msg.Payload(),
			Retained: msg.Retained(),
		}
	})
	if t.Wait() && t.Error() != nil {
		log.Fatal(t.Error())
	}

	return feed
}

// topicNotifier returns a channel which publishes all its messages to MQTT.
func topicNotifier(topic string, retain bool) chan<- interface{} {
	feed := make(chan interface{})

	go func() {
		for msg := range feed {
			sendToHub(topic, msg, retain)
		}
	}()

	return feed
}

// startHTTPServer starts the default HTTP server on the specified port.
func startHTTPServer(port string) {
	http.HandleFunc("/bar",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Hello, %q", r.URL.Path)
		})

	certFile := os.Getenv("HUB_HTTP_CERT")
	keyFile := os.Getenv("HUB_HTTP_KEY")

	if certFile != "" && keyFile != "" {
		log.Println("starting HTTPS (TLS) server at", port)
		log.Fatal(http.ListenAndServeTLS(port, certFile, keyFile, nil))
	} else {
		log.Println("starting HTTP server at", port)
		log.Fatal(http.ListenAndServe(port, nil))
	}
}

// startHeartbeat will send a timestamp every second to the specified topic.
func startHeartbeat(topic string) {
	feed := topicNotifier(topic, false)

	for {
		// synchronise as closely as possible to the exact next second
		time.Sleep(time.Duration(1e9 - time.Now().UnixNano()%1e9))

		// publish the heartbeat msg only if within 25ms of the second mark
		millis := time.Now().UnixNano() / 1e6
		if millis%1000 < 25 {
			feed <- millis
		} else {
			log.Println("missed heartbeat:", millis)
		}
	}
}
