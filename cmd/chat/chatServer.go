package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//ChatServer info
type ChatServer struct {
	conf                 *Configuration
	lsnr                 net.Listener
	clients              map[*client]struct{}
	clientCounter        int
	messageChan          chan *message
	writeClientsChan     chan *client
	deleteClientsChan    chan *client
	readAllUsernamesChan chan chan []string
}

//Configuration this should contain the responses from the server
type Configuration struct {
	Port                   int
	Addr                   string
	Transport              string
	CR                     string
	LF                     string
	CRLF                   string
	DefUsername            string
	ChanOpSymbol           string
	WhisperSymbol          string
	HasConnectedMsg        string
	HasDisconnectedMsg     string
	InfoMsg                string
	NormalMsg              string
	UsernameMsg            string
	WhisperMsg             string
	FailedUsernameMsg      string
	FailedwhisperMsg       string
	UnrecognisedCommandMsg string
	AcceptedLog            string
}

type message struct {
	username string
	//msgType  string //NORMAL;CHANOP;WHSIPER
	msgScope string //ALL;SENDERONLY,ALLEXCEPTSENDER
	text     string
}

type client struct {
	conn     net.Conn
	username string
}

// NewChatServer starts listening on port and returns an intialised server.
func NewChatServer(port int) (*ChatServer, error) {
	//get congif from json config file.
	configuration := Configuration{}
	err := GetConfigFromJSON("../../assets/config.json", &configuration)
	if err != nil {
		return nil, err
	}
	configuration.Port = port //override conf with param
	lsnr, e := net.Listen(configuration.Transport, ":"+strconv.Itoa(configuration.Port))
	if e != nil {
		return nil, e
	}

	log.Printf("Server started on port: %d", configuration.Port)

	return &ChatServer{&configuration,
			lsnr,
			make(map[*client]struct{}),
			0,
			make(chan *message, 1),
			make(chan *client),
			make(chan *client),
			make(chan chan []string)},
		err
}

// Start - accept client and send messages
func (s *ChatServer) Start() {
	go s.sendMessages()
	for {
		conn, e := s.lsnr.Accept()
		if e != nil {
			continue
		}
		go s.handleClient(conn)
	}
}

func (s *ChatServer) handleClient(conn net.Conn) {
	username := s.conf.DefUsername + strconv.Itoa(s.clientCounter)
	s.clientCounter++
	scanner := bufio.NewScanner(conn)
	c := &client{conn, username}
	s.writeToClients(c)
	s.sendWelcome(conn)

ForLoop:
	for scanner.Scan() {
		incoming := strings.NewReplacer(s.conf.CR, "", s.conf.LF, "").Replace(scanner.Text())
		if strings.HasPrefix(incoming, s.conf.ChanOpSymbol) {
			commands := strings.Split(incoming, " ")
			switch commands[0] {
			case "/bye":
				break ForLoop
			case "/list":
				usernames := s.readUsernamesFromClients()
				ul, _ := s.formatUserList(usernames)
				s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.InfoMsg, ul))
				continue
			case "/help":
				s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.InfoMsg, s.getWelcome()))
				continue
			case "/username":
				prevUsername := c.username
				newUsername := strings.NewReplacer(s.conf.CR, "", s.conf.LF, "", s.conf.DefUsername, "").Replace(commands[1])
				if uiu, _ := s.usernameInUse(newUsername); !uiu && len(newUsername) > 0 && strings.Index(newUsername, s.conf.ChanOpSymbol) == -1 {
					c.username = newUsername
					s.messageChan <- s.newMessage(c.username, "ALL", fmt.Sprintf(s.conf.UsernameMsg, prevUsername, newUsername))
					continue
				} else {
					s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.FailedUsernameMsg, prevUsername, newUsername))
					continue
				}
			default:
				s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.UnrecognisedCommandMsg, c.username, incoming))
				continue
			}
		} else if strings.HasPrefix(incoming, s.conf.WhisperSymbol) {
			if strings.Index(incoming, " ") > 1 && len(incoming) > 3 {
				whisperToUser := incoming[1:strings.Index(incoming, " ")]
				whisperMsg := incoming[strings.Index(incoming, " ")+1 : len(incoming)]
				if uiu, _ := s.usernameInUse(whisperToUser); uiu {
					s.messageChan <- s.newMessage(whisperToUser, "SENDERONLY", fmt.Sprintf(s.conf.WhisperMsg, c.username, whisperMsg))
					continue
				} else {
					s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.FailedwhisperMsg, whisperToUser))
					continue
				}
			} else {
				s.messageChan <- s.newMessage(c.username, "SENDERONLY", fmt.Sprintf(s.conf.UnrecognisedCommandMsg, c.username, incoming))
				continue
			}
		}
		//This is the main broadcast of a normal message.
		s.messageChan <- s.newMessage(c.username, "ALLEXCEPTSENDER", fmt.Sprintf(s.conf.NormalMsg, c.username, incoming))
	}
	//only reached when "break OuterLoop" (eg. /bye)
	s.deleteFromClients(c)
}

func (s *ChatServer) newMessage(toUsername, scope, text string) *message {
	return &message{toUsername, scope, text + s.conf.CRLF}
}

func (s *ChatServer) sendMessages() {
	for {
		select {
		case m := <-s.messageChan:
			// Loop over all connected clients
			for c := range s.clients {
				if m.msgScope == "SENDERONLY" {
					if c.username != m.username {
						continue
					}
				} else if m.msgScope == "ALLEXCEPTSENDER" {
					if c.username == m.username {
						continue
					}
				}
				go s.sendString(c.conn, m.text)
			}
			log.Printf("%s", m.text)
		case r := <-s.readAllUsernamesChan:
			u := make([]string, 0)
			for c := range s.clients {
				u = append(u, c.username)
			}
			r <- u
		case c := <-s.writeClientsChan:
			log.Printf(s.conf.AcceptedLog, c.username, c.conn.RemoteAddr())
			s.clients[c] = struct{}{}
			s.messageChan <- s.newMessage(c.username, "ALL", fmt.Sprintf(s.conf.HasConnectedMsg, c.username))
		case c := <-s.deleteClientsChan:
			s.messageChan <- s.newMessage(c.username, "ALL", fmt.Sprintf(s.conf.HasDisconnectedMsg, c.username))
			delete(s.clients, c)
			c.conn.Close()
		}
	}
}

func (s *ChatServer) writeToClients(c *client) {
	s.writeClientsChan <- c
}

func (s *ChatServer) deleteFromClients(c *client) {
	s.deleteClientsChan <- c
}

// readUsernamesFromClients change this
func (s *ChatServer) readUsernamesFromClients() []string {
	resp := make(chan []string)
	s.readAllUsernamesChan <- resp
	return <-resp
}

//formatUserList extracts,sorts adn returns a userlist string
func (s *ChatServer) formatUserList(usernames []string) (string, error) {
	//sort first
	sort.Strings(usernames)
	ul := "UserList:{"
	for _, username := range usernames {
		ul += fmt.Sprintf("%s, ", username)
	}
	ul = strings.TrimSuffix(ul, ", ") + fmt.Sprintf("} Total:[%d]", len(usernames))
	return ul, nil
}

//usernameInUse looksup a username returns true if found
func (s *ChatServer) usernameInUse(search string) (bool, error) {
	return StringInSlice(s.readUsernamesFromClients(), search)
}

//getWelcome gets the text from the banner file.
func (s *ChatServer) getWelcome() []byte {
	fileName, _ := filepath.Abs("../../assets/banner.txt")
	welcome, err := ioutil.ReadFile(fileName)
	if err != nil {
		panic(err)
	}
	return append(welcome, "\r\n"...)
}

func (s *ChatServer) sendWelcome(conn net.Conn) {
	//send welcome message in a goroutine so that the network doesnt block
	go s.sendString(conn, string(s.getWelcome()))
}

func (s *ChatServer) sendString(conn net.Conn, msg string) {
	_, err := conn.Write([]byte(msg))
	if err != nil {

	}
}
