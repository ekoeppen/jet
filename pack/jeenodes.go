package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/mitchellh/mapstructure"
)

func main() {
	// omit timestamps from the Log and send output to stdout
	log.SetFlags(log.Flags() & ^log.Ldate & ^log.Ltime)
	log.SetOutput(os.Stdout)

	// get some info from the environment, set when hub starts this pack
	packName := os.Getenv("HUB_PACK")
	if packName == "" {
		packName = "jeenodes"
	}
	mqttPort := os.Getenv("HUB_MQTT")
	if mqttPort == "" {
		mqttPort = "tcp://localhost:1883"
	}

	// normal pack startup begins here
	log.Println("[JET/Pack-jeenodes]")

	// connect to MQTT and wait for it before doing anything else
	connectToHub(packName, mqttPort, true)

	go configListener("packs/" + packName + "/decode/+/+")

	done := make(chan struct{})
	<-done // hang around forever
}

func configListener(feed string) {
	feedMap := map[string]<-chan event{}

	for evt := range topicWatcher(feed) {
		keys := strings.Split(evt.Topic, "/")
		if len(keys) != 5 {
			log.Println("decode:", keys, "?")
			continue
		}

		dev := keys[3]
		nid := keys[4]
		key := dev + "/" + nid

		// FIXME this is nonsense, needs a way to unsubscribe from MQTT
		//	right now, listeners can't be shut down or replaced once set up!
		if _, ok := feedMap[key]; ok {
			log.Println("decode:", keys, "closed")
			delete(feedMap, key)
			// FIXME close(c)
		}

		if len(evt.Payload) > 0 {
			var req []interface{}
			if evt.Decode(&req) {
				log.Println("decode:", keys, "fields:", req)
				feedMap[key] = topicWatcher("logger/" + dev + "/+")
				go newJeeNodeDecoder(feedMap[key], dev, nid, req)
			}
		}
	}
}

func newJeeNodeDecoder(in <-chan event, dev, nid string, fields []interface{}) {
	out := topicNotifier("jeenodes/"+dev+"/"+nid, false)
	defer close(out)

	prefix := "OK " + nid + " "
	for evt := range in {
		line := string(evt.Payload)
		if strings.HasPrefix(line, prefix) {
			keys := strings.Split(evt.Topic, "/")

			// set up a result map with some fields already filled in
			id, _ := strconv.Atoi(nid)
			ms, _ := strconv.Atoi(keys[2])
			result := map[string]interface{}{
				"dev":  dev,
				"id": id,
				"ms": ms,
			}

			// split the "OK <1> <2> ..." line into an array of strings
			bytes := strings.Split(line[3:], " ")
			off := uint(8) // start reading at bit 8, skipping the node ID byte

			// go through field definitions: +/- int, or string
			var value int64
			for _, field := range fields {
				if v, ok := field.(float64); ok {
					// it's a number, < 0 means treat as signed value
					n := uint(v)
					if v < 0 {
						n = uint(-v)
					}
					// extract the requested number of bits as little-endian
					value = 0
					residue := off % 8
					for fill := uint(0); fill < n + residue; fill += 8 {
						b, _ := strconv.Atoi(bytes[(fill+off)/8])
						value |= int64(b) << fill
					}
					off += n
					// drop extraneous lower and upper bits
					value >>= residue
					value &= (int64(1) << n) - 1
					// extend the sign if needed
					if v < 0 {
						value <<= 64-n
						value >>= 64-n
					}
				}
				if v, ok := field.(string); ok {
					result[v] = value
				}
			}
			out <- result
		}
	}
	log.Println("splitter EOF", dev, nid)
}

// TODO everything below is shared with the hub, should be in a common package!

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
	options.SetKeepAlive(10)
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
