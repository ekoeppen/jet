package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"errors"
	"math"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

type SwapFunction byte

const (
	STATUS  SwapFunction = 0
	QUERY                = 1
	COMMAND              = 2
)

type SwapValueType string

const (
	INT8    SwapValueType = "int8"
	UINT8                 = "uint8"
	INT16                 = "int16"
	UINT16                = "uint16"
	INT32                 = "int32"
	UINT32                = "uint32"
	FLOAT                 = "float"
	CSTRING               = "cstring"
	PSTRING               = "pstring"
)

type SwapPacket struct {
	RSSI		byte
	LQI		byte
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

type SwapRegister struct {
	Address byte
	RawData []byte
}

type SwapValue struct {
	Name     string
	Register byte
	Position byte
	Type     SwapValueType
	Unit     string
	Offset   int
	Scale    int
	RawData  []byte
}

func (value *SwapValue) SetRawData(data []byte) {
	var l byte
	switch value.Type {
	case INT8:
		l = 1
	case UINT8:
		l = 1
	case INT16:
		l = 2
	case UINT16:
		l = 2
	case INT32:
		l = 4
	case UINT32:
		l = 4
	}
	value.RawData = data[value.Position : value.Position+l]
}

func (value *SwapValue) AsInt() (n int64, err error) {
	switch value.Type {
	case INT8:
		n = int64(value.RawData[0])
	case UINT8:
		n = int64(value.RawData[0])
	case INT16:
		n = int64(binary.BigEndian.Uint16(value.RawData[0:2]))
	case UINT16:
		n = int64(binary.BigEndian.Uint16(value.RawData[0:2]))
	case INT32:
		n = int64(binary.BigEndian.Uint32(value.RawData[0:4]))
	case UINT32:
		n = int64(binary.BigEndian.Uint32(value.RawData[0:4]))
	default:
		err = errors.New("Value not an integer")
	}
	return
}

func (value *SwapValue) String() string {
	n, err := value.AsInt()
	if err == nil {
		if value.Scale == 1 {
			return fmt.Sprintf("%d", n - int64(value.Offset))
		} else {
			w := int(math.Log10(float64(value.Scale)))
			return fmt.Sprintf("%.*f", w, float64(n) / float64(value.Scale) - float64(value.Offset))
		}
	}
	return ""
}

type SwapMote struct {
	Address   byte
	Location  string
	Registers []SwapRegister
	Values    []SwapValue
}

func (mote *SwapMote) UpdateValues(p *SwapPacket) {
	for _, value := range mote.Values {
		if value.Register == p.RegisterID {
			value.SetRawData(p.Payload)
			log.Printf("/" + mote.Location + "/" + value.Name + ": " + value.String())
		}
	}
}

var motes []SwapMote

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
	log.Printf("SWAP pack starting, listening for %s, mote directory at %s\n", os.Args[1], os.Args[2])

	readMotes(os.Args[2])

	// connect to MQTT and wait for it before doing anything else
	connectToHub(packName, mqttPort, true)

	go swapListener(os.Args[1])

	done := make(chan struct{})
	<-done // hang around forever
}

func readMotes(moteDir string) {
	files, _ := filepath.Glob(moteDir + "/*")
	motes = make([]SwapMote, len(files))
	for i, file := range files {
		log.Printf("Reading from %s\n", file)
		viper.SetConfigFile(file)
		err := viper.ReadInConfig()
		if err != nil {
			log.Fatalf("Failed to parse mote config: %v\n", err)
		}
		viper.UnmarshalKey("general", &motes[i])
		viper.UnmarshalKey("values", &motes[i].Values)
		log.Printf("Mote %d: Location %s\n", motes[i].Address, motes[i].Location)
		for _, value := range motes[i].Values {
			log.Printf("    Value: %s, type: %v\n", value.Name, value.Type)
		}
	}
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

func handlePacket(p *SwapPacket) {
	for _, mote := range motes {
		if mote.Address == p.RegisterAddress {
			mote.UpdateValues(p)
		}
	}
}

func decodeSwapData(data []byte, p *SwapPacket) error {
	minLength := 1+2*2+1+7*2
	l := len(data)
	if l >= minLength {
		p.RSSI = hexBytesToByte(data[1:3])
		p.LQI = hexBytesToByte(data[3:5])
		p.Source = hexBytesToByte(data[6:8])
		p.Destination = hexBytesToByte(data[8:10])
		p.Hops = hexByteToByte(data[10])
		p.Security = hexByteToByte(data[11])
		p.Nonce = hexBytesToByte(data[12:14])
		p.Function = hexBytesToSwapFunction(data[14:16])
		p.RegisterAddress = hexBytesToByte(data[16:18])
		p.RegisterID = hexBytesToByte(data[18:20])
		if l > minLength {
			p.Payload = make([]byte, (l-minLength)/2, (l-minLength)/2)
			for i, _ := range p.Payload {
				p.Payload[i] = hexBytesToByte(data[minLength+i*2 : minLength+(i+1)*2])
			}
		}
		s, _ := json.Marshal(p)
		log.Printf("%s\n", s)
		return nil
	}
	return errors.New("Invalid SWAP data")
}

func swapListener(feed string) {
	for evt := range topicWatcher(feed) {
		var p SwapPacket
		if err := decodeSwapData(evt.Payload, &p); err == nil {
			handlePacket(&p)
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
