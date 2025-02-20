// main.go
package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	conn     *websocket.Conn
	isMain   bool
	id       string
	syncTime time.Time
}

type Message struct {
	Type     string `json:"type"`
	Command  string `json:"command"`
	Time     int64  `json:"time"`
	MainID   string `json:"mainId"`
	ClientID string `json:"clientId"`
}

var (
	clients    = make(map[*Client]bool)
	broadcast  = make(chan Message)
	register   = make(chan *Client)
	unregister = make(chan *Client)
	mainClient *Client
	upgrader   = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	mu sync.Mutex
)

func main() {
	// Serve static files
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/", fs)

	// WebSocket endpoint
	http.HandleFunc("/ws", handleConnections)

	// Start broadcast goroutine
	go handleMessages()

	log.Println("Server starting on :8080")
	err := http.ListenAndServe("0.0.0.0:8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func generateID() string {
	return time.Now().Format("20060102150405.000000000")
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
		return
	}

	clientID := generateID()
	client := &Client{
		conn:     ws,
		isMain:   false,
		id:       clientID,
		syncTime: time.Now(),
	}

	// Send ID to new client
	initMsg := Message{
		Type:     "init",
		ClientID: clientID,
	}
	client.conn.WriteJSON(initMsg)

	register <- client

	// If main client exists, notify new client
	mu.Lock()
	if mainClient != nil {
		client.conn.WriteJSON(Message{
			Type:   "main_status",
			MainID: mainClient.id,
		})
	}
	mu.Unlock()

	go handleClient(client)
}

func handleClient(client *Client) {
	defer func() {
		unregister <- client
		client.conn.Close()
	}()

	for {
		var msg Message
		err := client.conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("error: %v", err)
			break
		}

		msg.ClientID = client.id

		if msg.Type == "become_main" {
			mu.Lock()
			if mainClient == nil {
				client.isMain = true
				mainClient = client
				log.Printf("New main client: %s", client.id)

				// Notify all clients about new main client
				broadcast <- Message{
					Type:   "main_status",
					MainID: client.id,
				}
			}
			mu.Unlock()
			continue
		}

		if client.isMain {
			targetTime := time.Now().Add(100 * time.Millisecond)
			msg.Time = targetTime.UnixNano()
			log.Printf("Broadcasting command from main client: %s", msg.Command)
			broadcast <- msg
		}
	}
}

func handleMessages() {
	for {
		select {
		case client := <-register:
			mu.Lock()
			clients[client] = true
			log.Printf("New client connected: %s", client.id)
			mu.Unlock()

		case client := <-unregister:
			mu.Lock()
			if client.isMain && mainClient == client {
				mainClient = nil
				log.Printf("Main client disconnected: %s", client.id)
				// Notify all clients that main client is gone
				broadcast <- Message{
					Type:   "main_status",
					MainID: "",
				}
			}
			if _, ok := clients[client]; ok {
				delete(clients, client)
				log.Printf("Client disconnected: %s", client.id)
			}
			mu.Unlock()

		case msg := <-broadcast:
			mu.Lock()
			for client := range clients {
				err := client.conn.WriteJSON(msg)
				if err != nil {
					log.Printf("error: %v", err)
					client.conn.Close()
					delete(clients, client)
				}
			}
			mu.Unlock()
		}
	}
}
