package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
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
		packName = "swap"
	}
	mqttPort := os.Getenv("HUB_MQTT")
	if mqttPort == "" {
		mqttPort = "tcp://localhost:1883"
	}

	// normal pack startup begins here
	log.Printf("SWAP pack starting, listening for %s\n", os.Args[1])

	// connect to MQTT and wait for it before doing anything else
	connectToHub(packName, mqttPort, true)

	go swapListener(os.Args[1])

	done := make(chan struct{})
	<-done // hang around forever
}

type SwapFunction byte

const (
	STATUS  SwapFunction = 0
	QUERY                = 1
	COMMAND              = 2
)

type SwapPacket struct {
	Source          byte
	Destination     byte
	Hops            byte
	Security        byte
	Nonce           byte
	Function        SwapFunction
	RegisterAddress byte
	RegisterID      byte
	Payload         []byte
}

func hexByteToByte(data byte) byte {
	if data >= 97 {
		return data - 97 + 10
	} else if data >= 65 {
		return data - 65 + 10
	} else {
		return data - 48
	}
}

func hexBytesToByte(data []byte) byte {
	return hexByteToByte(data[0])*16 + hexByteToByte(data[1])
}

func hexBytesToSwapFunction(data []byte) SwapFunction {
	if data[0] == 48 {
		if data[1] == 48 {
			return STATUS
		} else if data[1] == 49 {
			return QUERY
		} else if data[1] == 50 {
			return COMMAND
		}
	}
	return STATUS
}

func swapListener(feed string) {
	//feedMap := map[string]<-chan event{}

	for evt := range topicWatcher(feed) {
		var p SwapPacket
		l := len(evt.Payload)
		if l >= 14 {
			p.Source = hexBytesToByte(evt.Payload[0:2])
			p.Destination = hexBytesToByte(evt.Payload[2:4])
			p.Hops = hexByteToByte(evt.Payload[4])
			p.Security = hexByteToByte(evt.Payload[5])
			p.Nonce = hexBytesToByte(evt.Payload[6:8])
			p.Function = hexBytesToSwapFunction(evt.Payload[8:10])
			p.RegisterAddress = hexBytesToByte(evt.Payload[10:12])
			p.RegisterID = hexBytesToByte(evt.Payload[12:14])
			if l > 14 {
				p.Payload = make([]byte, (l-14)/2, (l-14)/2)
				for i, _ := range p.Payload {
					p.Payload[i] = hexBytesToByte(evt.Payload[14+i*2 : 14+(i+1)*2])
				}
			}
			s, _ := json.Marshal(p)
			log.Printf("%s\n", s)
		}
	}
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
